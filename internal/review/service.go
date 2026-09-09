package review

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/FluentWork/fluentwork-backend/internal/corpus"
	"github.com/FluentWork/fluentwork-backend/internal/session"
)

// HitSource is optional B19 recent-hits lookup.
type HitSource interface {
	ListSessionHits(ctx context.Context, sessionID string) ([]corpus.RecentHit, error)
}

// Service runs B18 eval jobs and aggregates a session summary.
type Service struct {
	sessions session.Store
	hits     HitSource
	eval     *Evaluator
	logger   *slog.Logger
	interval time.Duration
	sleep    func(time.Duration)
	now      func() time.Time
	newID    func() string
}

// NewService constructs a review eval service.
// llm may be review.Completer, drill.Completer, or nil (fallback scores).
func NewService(sessions session.Store, hits HitSource, llm interface {
	Complete(ctx context.Context, prompt string) (string, error)
}, logger *slog.Logger,
) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	var evalLLM Completer
	if llm != nil {
		evalLLM = llm
	}
	return &Service{
		sessions: sessions,
		hits:     hits,
		eval:     &Evaluator{LLM: evalLLM},
		logger:   logger.With("component", "review.eval"),
		interval: DefaultQPSInterval,
		sleep:    time.Sleep,
		now:      time.Now,
		newID:    uuid.NewString,
	}
}

// SetInterval overrides the 1 QPS gap (tests use 0).
func (s *Service) SetInterval(d time.Duration) {
	if s != nil {
		s.interval = d
	}
}

// SetSleep overrides time.Sleep for rate-limit tests.
func (s *Service) SetSleep(fn func(time.Duration)) {
	if s != nil && fn != nil {
		s.sleep = fn
	}
}

// EnqueueEval writes a session.eval job. Idempotent if one is already active or done.
func (s *Service) EnqueueEval(ctx context.Context, sessionID string) error {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return nil
	}
	exists, err := s.sessions.HasSessionJob(ctx, sessionID, session.JobTypeSessionEval,
		session.JobStatusPending, session.JobStatusProcessing, session.JobStatusDone)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	now := s.now().UTC()
	return s.sessions.EnqueueJob(ctx, session.Job{
		ID:          s.newID(),
		SessionID:   sessionID,
		JobType:     session.JobTypeSessionEval,
		Status:      session.JobStatusPending,
		AvailableAt: now,
		CreatedAt:   now,
		UpdatedAt:   now,
	})
}

// RunEvalJob scores user utterances at 1 QPS and writes llm_eval_json.
func (s *Service) RunEvalJob(ctx context.Context, sessionID string) error {
	utts, err := s.sessions.ListUtterances(ctx, sessionID)
	if err != nil {
		return err
	}
	var hits []corpus.RecentHit
	if s.hits != nil {
		hits, err = s.hits.ListSessionHits(ctx, sessionID)
		if err != nil {
			s.logger.Warn("list session hits", "session_id", sessionID, "err", err)
			hits = nil
		}
	}
	first := true
	for _, utt := range utts {
		if utt.Speaker != session.SpeakerUser {
			continue
		}
		if len(utt.LLMEvalJSON) > 0 {
			continue
		}
		if !first && s.interval > 0 {
			s.sleep(s.interval)
		}
		first = false
		result := s.eval.Evaluate(ctx, utt, hits)
		raw, err := json.Marshal(result)
		if err != nil {
			return err
		}
		if err := s.sessions.SaveUtteranceEval(ctx, utt.ID, raw); err != nil {
			return err
		}
		if err := ctx.Err(); err != nil {
			return err
		}
	}
	return nil
}

// Summary aggregates per-utterance eval into a session-level payload.
func (s *Service) Summary(ctx context.Context, sessionID string) (*session.EvalSummary, error) {
	utts, err := s.sessions.ListUtterances(ctx, sessionID)
	if err != nil {
		return nil, err
	}
	user := make([]session.Utterance, 0, len(utts))
	for _, utt := range utts {
		if utt.Speaker == session.SpeakerUser {
			user = append(user, utt)
		}
	}
	if len(user) == 0 {
		return &session.EvalSummary{Score: 0, Suggestions: []string{}, Complete: true}, nil
	}
	evals := make([]UtteranceEval, 0, len(user))
	for _, utt := range user {
		if len(utt.LLMEvalJSON) == 0 {
			return &session.EvalSummary{UtteranceN: len(user), Suggestions: []string{}, Complete: false}, nil
		}
		var one UtteranceEval
		if err := json.Unmarshal(utt.LLMEvalJSON, &one); err != nil {
			return &session.EvalSummary{UtteranceN: len(user), Suggestions: []string{}, Complete: false}, nil
		}
		evals = append(evals, one)
	}
	n := float64(len(evals))
	sum := session.EvalSummary{UtteranceN: len(evals), Complete: true, Suggestions: []string{}}
	seen := map[string]struct{}{}
	for _, ev := range evals {
		sum.Score += ev.Score
		sum.Dims.Grammar += ev.Dims.Grammar
		sum.Dims.Fluency += ev.Dims.Fluency
		sum.Dims.Vocabulary += ev.Dims.Vocabulary
		for _, tip := range ev.Suggestions {
			if _, ok := seen[tip]; ok {
				continue
			}
			seen[tip] = struct{}{}
			sum.Suggestions = append(sum.Suggestions, tip)
			if len(sum.Suggestions) == 3 {
				break
			}
		}
	}
	sum.Score /= n
	sum.Dims.Grammar /= n
	sum.Dims.Fluency /= n
	sum.Dims.Vocabulary /= n
	return &sum, nil
}
