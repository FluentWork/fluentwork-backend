package voicegateway_test

import (
	"context"
	"testing"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/session"
)

type debugSource struct {
	candidates []session.BlockCandidate
	err        error
}

func (s *debugSource) CandidatesForUser(_ context.Context, _ string) ([]session.BlockCandidate, error) {
	return s.candidates, s.err
}

func TestDebug_SimpleHit(t *testing.T) {
	src := &debugSource{
		candidates: []session.BlockCandidate{
			{ID: "block-1", ExpressionEN: "ship it", IntentZH: "推进"},
		},
	}
	det := session.NewHitDetector(src)

	ctx, cancel := context.WithTimeout(context.Background(), 500*time.Millisecond)
	defer cancel()

	decision, err := det.Detect(ctx, session.HitDetectRequest{
		UserID:    "u1",
		SessionID: "sess-1",
		TurnID:    "turn-9",
		Text:      "ship it today",
	})
	if err != nil {
		t.Fatalf("Detect error: %v", err)
	}
	if decision.Kind != session.HitDecisionHit {
		t.Fatalf("Expected hit, got %v (duration=%v)", decision.Kind, decision.Duration)
	}
	t.Logf("Hit: id=%s score=%.3f label=%q", decision.Hit.ID, decision.Hit.Score, decision.Hit.BadgeLabel)
}
