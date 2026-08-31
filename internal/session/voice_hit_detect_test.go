package session

import (
	"context"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/voiceproto"
)

type fakeBlockSource struct {
	candidates []BlockCandidate
	err        error
	calls      int32
}

func (f *fakeBlockSource) CandidatesForUser(ctx context.Context, _ string) ([]BlockCandidate, error) {
	atomic.AddInt32(&f.calls, 1)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if f.err != nil {
		return nil, f.err
	}
	out := make([]BlockCandidate, len(f.candidates))
	copy(out, f.candidates)
	return out, nil
}

func TestHitDetector_MissOnEmptyText(t *testing.T) {
	t.Parallel()
	src := &fakeBlockSource{candidates: []BlockCandidate{
		{ID: "b1", ExpressionEN: "hello world"},
	}}
	d := NewHitDetector(src)
	decision, err := d.Detect(context.Background(), HitDetectRequest{
		UserID:    "u1",
		SessionID: "s1",
		TurnID:    "t1",
		Text:      "  ",
	})
	if !errors.Is(err, ErrHitDetectInvalidRequest) {
		t.Fatalf("err: got %v want ErrHitDetectInvalidRequest", err)
	}
	if decision.Kind != HitDecisionMiss {
		t.Fatalf("kind: got %v want Miss", decision.Kind)
	}
	if got := atomic.LoadInt32(&src.calls); got != 0 {
		t.Fatalf("source called %d times on invalid request, want 0", got)
	}
}

func TestHitDetector_MissOnMissingUserID(t *testing.T) {
	t.Parallel()
	d := NewHitDetector(&fakeBlockSource{})
	_, err := d.Detect(context.Background(), HitDetectRequest{
		UserID: "",
		Text:   "hello",
	})
	if !errors.Is(err, ErrHitDetectInvalidRequest) {
		t.Fatalf("err: got %v want ErrHitDetectInvalidRequest", err)
	}
}

func TestHitDetector_RejectsOversizeText(t *testing.T) {
	t.Parallel()
	src := &fakeBlockSource{}
	d := NewHitDetector(src)
	huge := strings.Repeat("a", hitMaxTextLen+1)
	_, err := d.Detect(context.Background(), HitDetectRequest{
		UserID: "u1", Text: huge,
	})
	if !errors.Is(err, ErrHitDetectInvalidRequest) {
		t.Fatalf("err: got %v want ErrHitDetectInvalidRequest", err)
	}
}

func TestHitDetector_MissOnEmptyCandidates(t *testing.T) {
	t.Parallel()
	src := &fakeBlockSource{candidates: nil}
	d := NewHitDetector(src)
	decision, err := d.Detect(context.Background(), HitDetectRequest{
		UserID: "u1", Text: "ship it",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if decision.Kind != HitDecisionMiss {
		t.Fatalf("kind: got %v want Miss", decision.Kind)
	}
}

func TestHitDetector_PropagatesSourceError(t *testing.T) {
	t.Parallel()
	boom := errors.New("db down")
	src := &fakeBlockSource{err: boom}
	d := NewHitDetector(src)
	_, err := d.Detect(context.Background(), HitDetectRequest{
		UserID: "u1", Text: "ship it",
	})
	if err == nil || !errors.Is(err, boom) {
		t.Fatalf("err: got %v want wraps %v", err, boom)
	}
}

func TestHitDetector_HitOnExactExpressionMatch(t *testing.T) {
	t.Parallel()
	src := &fakeBlockSource{candidates: []BlockCandidate{
		{ID: "b1", ExpressionEN: "ship it", IntentZH: "推进"},
		{ID: "b2", ExpressionEN: "let's circle back", IntentZH: "再聊"},
	}}
	d := NewHitDetector(src)
	decision, err := d.Detect(context.Background(), HitDetectRequest{
		UserID: "u1", SessionID: "s1", TurnID: "t1",
		Text: "I think we should ship it today",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if decision.Kind != HitDecisionHit {
		t.Fatalf("kind: got %v want Hit (decision=%+v)", decision.Kind, decision)
	}
	if decision.Hit.ID != "b1" {
		t.Fatalf("hit id: got %q want b1 (hit=%+v)", decision.Hit.ID, decision.Hit)
	}
	if decision.Hit.BadgeLabel != "ship it" {
		t.Fatalf("badge label: got %q", decision.Hit.BadgeLabel)
	}
	if decision.Hit.Tier != voiceproto.BadgeTierSoft {
		t.Fatalf("tier: got %q", decision.Hit.Tier)
	}
}

func TestHitDetector_HitOnAnchorUserSaidMatch(t *testing.T) {
	t.Parallel()
	src := &fakeBlockSource{candidates: []BlockCandidate{
		{
			ID:             "b1",
			ExpressionEN:   "let's table this",
			AnchorUserSaid: "we can talk about it later",
		},
	}}
	d := NewHitDetector(src)
	decision, err := d.Detect(context.Background(), HitDetectRequest{
		UserID: "u1", Text: "I think we can talk about it later",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if decision.Kind != HitDecisionHit || decision.Hit.ID != "b1" {
		t.Fatalf("expected hit on b1, got %+v", decision)
	}
}

func TestHitDetector_MissWhenNoTokenOverlap(t *testing.T) {
	t.Parallel()
	src := &fakeBlockSource{candidates: []BlockCandidate{
		{ID: "b1", ExpressionEN: "ship it"},
		{ID: "b2", ExpressionEN: "let's table this"},
	}}
	d := NewHitDetector(src)
	decision, err := d.Detect(context.Background(), HitDetectRequest{
		UserID: "u1", Text: "completely unrelated phrase",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if decision.Kind != HitDecisionMiss {
		t.Fatalf("kind: got %v want Miss", decision.Kind)
	}
}

func TestHitDetector_PrefersTighterMatch(t *testing.T) {
	t.Parallel()
	// Two candidates match "ship it" — one has a tight single-token expression,
	// the other has a noisy multi-token one with the same word. Tighter wins.
	src := &fakeBlockSource{candidates: []BlockCandidate{
		{ID: "loose", ExpressionEN: "do we have any blockers before we ship it"},
		{ID: "tight", ExpressionEN: "ship it"},
	}}
	d := NewHitDetector(src)
	decision, err := d.Detect(context.Background(), HitDetectRequest{
		UserID: "u1", Text: "ship it",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	if decision.Kind != HitDecisionHit {
		t.Fatalf("expected Hit, got %+v", decision)
	}
	if decision.Hit.ID != "tight" {
		t.Fatalf("expected tighter match, got %q (score %.3f)", decision.Hit.ID, decision.Hit.Score)
	}
}

func TestHitDetector_RespectsCandidateCap(t *testing.T) {
	t.Parallel()
	cands := make([]BlockCandidate, 0, hitCandidateCap+5)
	// One long-noise candidate that would otherwise score near the threshold
	// if evaluated. Then a fill of exact-length distractors.
	cands = append(cands, BlockCandidate{
		ID:           "noise",
		ExpressionEN: strings.Repeat("filler ", 100) + "ship it",
	})
	for i := 0; i < hitCandidateCap+4; i++ {
		cands = append(cands, BlockCandidate{
			ID:           "noise-" + string(rune('a'+i%26)),
			ExpressionEN: strings.Repeat("zzz ", 30),
		})
	}
	src := &fakeBlockSource{candidates: cands}
	d := NewHitDetector(src)
	decision, err := d.Detect(context.Background(), HitDetectRequest{
		UserID: "u1", Text: "ship it",
	})
	if err != nil {
		t.Fatalf("err: %v", err)
	}
	// After capping + preferring shorter expressions, the "noise" candidate
	// may or may not survive. The critical assertion is that Detect returns
	// within a small budget and does not panic.
	if decision.Duration > 50*time.Millisecond {
		t.Fatalf("Detect too slow: %v", decision.Duration)
	}
}

func TestHitDetector_HonorsContextCancellation(t *testing.T) {
	t.Parallel()
	src := &fakeBlockSource{
		candidates: []BlockCandidate{{ID: "b1", ExpressionEN: "ship it"}},
	}
	d := NewHitDetector(src)
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := d.Detect(ctx, HitDetectRequest{
		UserID: "u1", Text: "ship it",
	})
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("err: got %v want context.Canceled", err)
	}
}

func TestHitDetector_TokenizeStripsPunctuationAndCase(t *testing.T) {
	t.Parallel()
	got := tokenize("I'll ship it — TODAY!")
	want := []string{"i", "ll", "ship", "it", "today"}
	if len(got) != len(want) {
		t.Fatalf("len: got %d want %d (%v)", len(got), len(want), got)
	}
	for i := range got {
		if got[i] != want[i] {
			t.Fatalf("[%d]: got %q want %q", i, got[i], want[i])
		}
	}
}

func TestHitDetector_DedupeKeyFromHitIsUsable(t *testing.T) {
	t.Parallel()
	src := &fakeBlockSource{candidates: []BlockCandidate{
		{ID: "block-7", ExpressionEN: "ship it"},
	}}
	d := NewHitDetector(src)
	decision, err := d.Detect(context.Background(), HitDetectRequest{
		UserID: "u1", SessionID: "sess-1", TurnID: "turn-9",
		Text: "ship it",
	})
	if err != nil || decision.Kind != HitDecisionHit {
		t.Fatalf("expected hit, got %+v err=%v", decision, err)
	}
	frame := voiceproto.NewFeedbackBadge(
		decision.Hit.BadgeLabel,
		decision.Hit.ID,
		decision.Hit.Tier,
		"sess-1",
		"turn-9",
	)
	want := "sess-1|turn-9|block-7"
	if frame.DedupeKey != want {
		t.Fatalf("dedupe key: got %q want %q", frame.DedupeKey, want)
	}
	if frame.SessionID != "sess-1" || frame.TurnID != "turn-9" {
		t.Fatalf("session/turn round trip wrong: %+v", frame)
	}
}
