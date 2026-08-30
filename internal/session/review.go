package session

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/FluentWork/fluentwork-backend/pkg/logx"
)

const defaultJobRetryDelay = 2 * time.Second

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
	reviewJSON, err := buildStubReviewJSON(session, utterances)
	if err != nil {
		pipelineErr = err
		return pipelineErr
	}
	_, err = s.store.MarkSessionReviewed(ctx, sessionID, reviewJSON, s.now().UTC())
	if err != nil {
		pipelineErr = err
		return pipelineErr
	}
	endAttrs = []any{
		"status", StatusReviewed,
		"duration_sec", session.DurationSec,
		"utterance_count", len(utterances),
		"review_bytes", len(reviewJSON),
		"generator", "stub-v1",
	}
	return nil
}

type stubReview struct {
	Version        int      `json:"version"`
	Status         string   `json:"status"`
	Summary        string   `json:"summary"`
	UtteranceCount int      `json:"utterance_count"`
	DurationSec    int      `json:"duration_sec"`
	Highlights     []string `json:"highlights"`
	Generator      string   `json:"generator"`
}

func buildStubReviewJSON(session Session, utterances []Utterance) ([]byte, error) {
	doc := stubReview{
		Version:        1,
		Status:         "ready",
		Summary:        "Practice session completed. Full model review lands with the production LLM path.",
		UtteranceCount: len(utterances),
		DurationSec:    session.DurationSec,
		Highlights:     []string{},
		Generator:      "stub-v1",
	}
	return json.Marshal(doc)
}
