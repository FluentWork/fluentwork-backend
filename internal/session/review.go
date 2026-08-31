package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/aicost"
	"github.com/FluentWork/fluentwork-backend/internal/reviewgen"
	"github.com/FluentWork/fluentwork-backend/pkg/logx"
)

const defaultJobRetryDelay = 2 * time.Second

const (
	stubReviewGenerator   = "stub-v1"
	legacyReviewGenerator = "legacy-review-v0"
)

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
	_, err = s.store.MarkSessionReviewed(ctx, sessionID, artifacts.ReviewJSON, s.now().UTC())
	if err != nil {
		pipelineErr = err
		return pipelineErr
	}
	if err := s.recordReviewCost(ctx, session, artifacts); err != nil {
		pipelineErr = err
		return pipelineErr
	}
	endAttrs = []any{
		"status", StatusReviewed,
		"duration_sec", session.DurationSec,
		"utterance_count", len(utterances),
		"review_bytes", len(artifacts.ReviewJSON),
		"generator", artifacts.Generator,
	}
	return nil
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

	result, err := s.reviewGen.Generate(ctx, reviewgen.Request{
		SessionID:  session.ID,
		UserID:     session.UserID,
		SceneType:  session.SceneType,
		Transcript: renderTranscript(utterances),
	})
	if err != nil {
		s.logger.Warn("review generator failed; falling back to stub",
			"session_id", session.ID,
			"user_id", session.UserID,
			"scene_type", session.SceneType,
			"stage", "orchestration",
			"err", err,
		)
		return buildStubReviewArtifacts(session, utterances)
	}
	return reviewArtifacts{
		ReviewJSON: buildReviewPayload(result.Review, result.Refine, session, result.Generator),
		RefineJSON: append([]byte(nil), result.Refine...),
		Generator:  result.Generator,
		Cost: &aicost.RecordRequest{
			TaskType:  "review.eval",
			Model:     result.Model,
			TokensIn:  result.TokensIn,
			TokensOut: result.TokensOut,
			CostFen:   0,
		},
	}, nil
}

func (s *Service) recordReviewCost(ctx context.Context, session Session, artifacts reviewArtifacts) error {
	if artifacts.Cost == nil {
		s.logger.Info("ai cost skipped",
			"session_id", session.ID,
			"user_id", session.UserID,
			"generator", artifacts.Generator,
			"task_type", "review.eval",
			"stage", "billing",
			"reason", "no_ai_usage",
		)
		return nil
	}
	if s.costRecorder == nil {
		return fmt.Errorf("aicost recorder is required for generator %q", artifacts.Generator)
	}
	req := *artifacts.Cost
	if strings.TrimSpace(req.UserID) == "" {
		req.UserID = session.UserID
	}
	if _, err := s.costRecorder.Record(ctx, req); err != nil {
		return fmt.Errorf("record ai cost: %w", err)
	}
	return nil
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
