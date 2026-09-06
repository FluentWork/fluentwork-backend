package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/FluentWork/fluentwork-backend/internal/aicost"
	"github.com/FluentWork/fluentwork-backend/internal/reviewgen"
	"github.com/FluentWork/fluentwork-backend/pkg/logx"
)

const defaultJobRetryDelay = 2 * time.Second

const (
	stubReviewGenerator   = "stub-v1"
	legacyReviewGenerator = "legacy-review-v0"
)

// #21 (B8 followup) — Ark Mini per-million-token pricing in 元 (CNY).
// These are the public list prices for Volcano Ark Doubao models as of 2026-09:
//   input  : 0.3 CNY / 1M tokens
//   output : 0.6 CNY / 1M tokens
// CostFen is recorded in 分 (1 元 = 100 分), so the multiplier is 0.1 * 100 / 1_000_000.
// When Ark introduces model-specific pricing, switch on result.Model.
const (
	arkReviewTaskType    = "review.eval"
	arkInputFenPerToken  = 0.03 / 1_000_000.0 // 0.3元/M token → 0.03分/token (rounded)
	arkOutputFenPerToken = 0.06 / 1_000_000.0 // 0.6元/M token → 0.06分/token (rounded)
)

// reviewRetryAttempts is the number of times buildReviewArtifacts will retry a
// failed generator call before falling back to stub artifacts. Acceptance
// criterion: "失败重试 1 次" — first try plus one retry = 2 total attempts.
const reviewRetryAttempts = 2

// ProcessNextJob claims and processes one pending session.finished job.
// ok=false means the queue was empty.
func (s *Service) ProcessNextJob(ctx context.Context, workerID string) (ok bool, err error) {
	workerID = strings.TrimSpace(workerID)
	if workerID == "" {
		return false, fmt.Errorf("worker id is required")
	}
	now := s.now().UTC()
	job, err := s.store.ClaimNextJob(ctx, workerID, now)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return false, nil
		}
		return false, err
	}

	// Detach from worker shutdown cancel so Fail/Complete can still settle state,
	// while still bounding the actual job work with a deadline.
	jobCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), DefaultJobTimeout)
	defer cancel()
	seg := logx.Begin(s.logger, "session.job.process",
		"job_id", job.ID,
		"session_id", job.SessionID,
		"job_type", job.JobType,
		"attempts", job.Attempts,
	)
	defer func() {
		seg.End(err)
	}()

	if err := s.runJob(jobCtx, job); err != nil {
		s.logger.Warn("session job failed",
			"job_id", job.ID,
			"session_id", job.SessionID,
			"attempts", job.Attempts,
			"err", err,
		)
		if failErr := s.failJob(ctx, job.ID, now, err.Error()); failErr != nil {
			return true, fmt.Errorf("fail job: %w (original: %v)", failErr, err)
		}
		return true, err
	}
	if err := s.completeJob(ctx, job.ID, now); err != nil {
		s.logger.Warn("session job complete failed",
			"job_id", job.ID,
			"session_id", job.SessionID,
			"err", err,
		)
		if failErr := s.failJob(ctx, job.ID, now, err.Error()); failErr != nil {
			return true, fmt.Errorf("fail job after complete error: %w (original: %v)", failErr, err)
		}
		return true, err
	}
	s.logger.Info("session job completed",
		"job_id", job.ID,
		"session_id", job.SessionID,
		"job_type", job.JobType,
	)
	return true, nil
}

func (s *Service) completeJob(parent context.Context, jobID string, at time.Time) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
	defer cancel()
	return s.store.CompleteJob(ctx, jobID, at)
}

func (s *Service) failJob(parent context.Context, jobID string, at time.Time, errMsg string) error {
	ctx, cancel := context.WithTimeout(context.WithoutCancel(parent), 5*time.Second)
	defer cancel()
	return s.store.FailJob(ctx, jobID, at, errMsg, defaultJobRetryDelay)
}

func (s *Service) runJob(ctx context.Context, job Job) error {
	switch job.JobType {
	case JobTypeSessionFinished:
		return s.processSessionFinished(ctx, job.SessionID)
	default:
		return fmt.Errorf("unsupported job type %q", job.JobType)
	}
}

func (s *Service) processSessionFinished(ctx context.Context, sessionID string) error {
	seg := logx.Begin(s.logger, "session.review.pipeline",
		"session_id", sessionID,
		"stage", "orchestration",
	)
	var pipelineErr error
	var endAttrs []any
	defer func() {
		seg.End(pipelineErr, endAttrs...)
	}()

	session, err := s.store.GetSession(ctx, sessionID)
	if err != nil {
		pipelineErr = err
		return pipelineErr
	}
	if session.Status == StatusReviewed {
		endAttrs = []any{"status", session.Status}
		return nil
	}
	if session.Status != StatusEnded {
		pipelineErr = fmt.Errorf("session status %q is not ended", session.Status)
		return pipelineErr
	}
	utterances, err := s.store.ListUtterances(ctx, sessionID)
	if err != nil {
		pipelineErr = err
		return pipelineErr
	}
	artifacts, err := s.buildReviewArtifacts(ctx, session, utterances)
	if err != nil {
		pipelineErr = err
		return pipelineErr
	}
	now := s.now().UTC()
	// #21 (B8 followup): atomic review + cost. When the generator produced a
	// real artifact we use MarkSessionReviewedWithCost to commit review_json
	// and ai_cost_logs in one transaction. When the generator was unavailable
	// or both attempts failed we fall back to MarkSessionReviewed with no cost
	// (stub artifacts: Cost == nil) — no cost ledger row should be written for
	// "no real AI usage" sessions.
	if artifacts.Cost == nil {
		_, err = s.store.MarkSessionReviewed(ctx, sessionID, artifacts.ReviewJSON, now)
	} else {
		costLog := buildCostLog(session, artifacts, now)
		_, err = s.store.MarkSessionReviewedWithCost(ctx, sessionID, artifacts.ReviewJSON, now, costLog)
	}
	if err != nil {
		pipelineErr = err
		return pipelineErr
	}
	endAttrs = []any{
		"status", StatusReviewed,
		"duration_sec", session.DurationSec,
		"utterance_count", len(utterances),
		"review_bytes", len(artifacts.ReviewJSON),
		"generator", artifacts.Generator,
		"cost_recorded", artifacts.Cost != nil,
	}
	return nil
}

// buildCostLog converts a reviewArtifacts.Cost (RecordRequest) into an aicost.Log
// with a fresh ID and timestamp, ready for atomic insert alongside the review.
func buildCostLog(session Session, artifacts reviewArtifacts, at time.Time) aicost.Log {
	req := *artifacts.Cost
	userID := strings.TrimSpace(req.UserID)
	if userID == "" {
		userID = session.UserID
	}
	return aicost.Log{
		ID:        uuid.NewString(),
		TaskType:  arkReviewTaskType,
		Model:     strings.TrimSpace(req.Model),
		TokensIn:  req.TokensIn,
		TokensOut: req.TokensOut,
		AudioSec:  req.AudioSec,
		CostFen:   computeCostFen(req.TokensIn, req.TokensOut),
		CreatedAt: at,
		UserID:    nullableUserID(userID),
	}
}

// nullableUserID returns &id only when id is non-empty; otherwise nil.
// Matches nullableString in aicost.MySQLStore.
func nullableUserID(id string) *string {
	if strings.TrimSpace(id) == "" {
		return nil
	}
	copied := id
	return &copied
}

// computeCostFen converts token counts into a cost in 分 (1元 = 100分) using
// the Ark Mini pricing table. The math is intentionally simple — when Ark
// ships model-specific pricing we should switch on result.Model here.
func computeCostFen(tokensIn, tokensOut int) int {
	if tokensIn < 0 {
		tokensIn = 0
	}
	if tokensOut < 0 {
		tokensOut = 0
	}
	fen := float64(tokensIn)*arkInputFenPerToken + float64(tokensOut)*arkOutputFenPerToken
	if fen < 0 {
		fen = 0
	}
	return int(fen + 0.5) // round-half-up to nearest 分
}

type reviewArtifacts struct {
	ReviewJSON []byte
	RefineJSON []byte
	Generator  string
	Cost       *aicost.RecordRequest
}

func buildStubReviewArtifacts(session Session, utterances []Utterance) (reviewArtifacts, error) {
	doc := map[string]any{
		"goal_achievement": map[string]any{
			"met":  false,
			"note": "Practice session completed. Full model review is unavailable; transcript and session status are still available.",
		},
		"issues": []any{},
		"suggestions": []map[string]any{
			{"text": "Review generation is unavailable right now. You can still replay the transcript and retry later."},
		},
		"comparisons":     []any{},
		"utterance_count": len(utterances),
	}
	reviewJSON, err := json.Marshal(doc)
	if err != nil {
		return reviewArtifacts{}, err
	}
	refineJSON := json.RawMessage(`{"blocks":[]}`)
	return reviewArtifacts{
		ReviewJSON: buildReviewPayload(reviewJSON, refineJSON, session, stubReviewGenerator),
		RefineJSON: refineJSON,
		Generator:  stubReviewGenerator,
		Cost:       nil,
	}, nil
}

func (s *Service) buildReviewArtifacts(ctx context.Context, session Session, utterances []Utterance) (reviewArtifacts, error) {
	if s.reviewGen == nil {
		return buildStubReviewArtifacts(session, utterances)
	}

	// #21 (B8 followup) — retry the generator up to reviewRetryAttempts total
	// times before falling back to stub artifacts. Acceptance criterion:
	// "失败重试 1 次" — first try plus one retry = 2 total attempts.
	var (
		result reviewgen.Result
		err    error
	)
	for attempt := 1; attempt <= reviewRetryAttempts; attempt++ {
		result, err = s.reviewGen.Generate(ctx, reviewgen.Request{
			SessionID:  session.ID,
			UserID:     session.UserID,
			SceneType:  session.SceneType,
			Transcript: renderTranscript(utterances),
		})
		if err == nil {
			break
		}
		s.logger.Warn("review generator attempt failed",
			"session_id", session.ID,
			"user_id", session.UserID,
			"scene_type", session.SceneType,
			"stage", "orchestration",
			"attempt", attempt,
			"max_attempts", reviewRetryAttempts,
			"err", err,
		)
	}
	if err != nil {
		s.logger.Warn("review generator failed after retries; falling back to stub",
			"session_id", session.ID,
			"user_id", session.UserID,
			"scene_type", session.SceneType,
			"stage", "orchestration",
			"attempts", reviewRetryAttempts,
			"err", err,
		)
		return buildStubReviewArtifacts(session, utterances)
	}
	return reviewArtifacts{
		ReviewJSON: buildReviewPayload(result.Review, result.Refine, session, result.Generator),
		RefineJSON: append([]byte(nil), result.Refine...),
		Generator:  result.Generator,
		Cost: &aicost.RecordRequest{
			TaskType:  arkReviewTaskType,
			Model:     result.Model,
			TokensIn:  result.TokensIn,
			TokensOut: result.TokensOut,
			CostFen:   0,
		},
	}, nil
}

func renderTranscript(utterances []Utterance) string {
	if len(utterances) == 0 {
		return ""
	}
	var lines []string
	for _, utterance := range utterances {
		text := strings.TrimSpace(utterance.Text)
		if text == "" {
			continue
		}
		speaker := strings.TrimSpace(utterance.Speaker)
		if speaker == "" {
			speaker = SpeakerUser
		}
		lines = append(lines, fmt.Sprintf("%s: %s", speaker, text))
	}
	return strings.Join(lines, "\n")
}

func buildReviewPayload(review, refine []byte, session Session, generator string) []byte {
	payload := map[string]any{
		"review":    rawJSONOrEmptyObject(review),
		"refine":    rawJSONOrDefault(refine, json.RawMessage(`{"blocks":[]}`)),
		"generator": strings.TrimSpace(generator),
		"status":    "ready",
	}
	if session.DurationSec > 0 {
		payload["duration_sec"] = session.DurationSec
	}
	encoded, err := json.Marshal(payload)
	if err != nil {
		return append([]byte(nil), review...)
	}
	return encoded
}

func rawJSONOrEmptyObject(raw []byte) any {
	return rawJSONOrDefault(raw, json.RawMessage(`{}`))
}

func rawJSONOrDefault(raw []byte, fallback json.RawMessage) any {
	var out any
	if err := json.Unmarshal(raw, &out); err != nil {
		_ = json.Unmarshal(fallback, &out)
	}
	return out
}
