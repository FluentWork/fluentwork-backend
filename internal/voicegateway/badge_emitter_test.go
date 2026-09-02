package voicegateway

import (
	"context"
	"encoding/json"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/session"
	"github.com/FluentWork/fluentwork-backend/internal/voiceproto"
)

type stubBlockSource struct {
	mu         sync.Mutex
	candidates []session.BlockCandidate
	err        error
	delay      time.Duration
	calls      int32
}

func newStubSource(candidates ...session.BlockCandidate) *stubBlockSource {
	return &stubBlockSource{candidates: candidates}
}

func withDelay(candidates []session.BlockCandidate, delay time.Duration) *stubBlockSource {
	return &stubBlockSource{candidates: candidates, delay: delay}
}

func (s *stubBlockSource) CandidatesForUser(ctx context.Context, _ string) ([]session.BlockCandidate, error) {
	atomic.AddInt32(&s.calls, 1)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	if s.delay > 0 {
		select {
		case <-time.After(s.delay):
		case <-ctx.Done():
			return nil, ctx.Err()
		}
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return nil, s.err
	}
	out := make([]session.BlockCandidate, len(s.candidates))
	copy(out, s.candidates)
	return out, nil
}

// fakeWSConn captures every JSON frame written via WriteBadge for assertion
// without needing a real WebSocket connection.
type fakeWSConn struct {
	written [][]byte
}

func (f *fakeWSConn) WriteBadge(_ context.Context, raw []byte) error {
	f.written = append(f.written, append([]byte(nil), raw...))
	return nil
}

func newTestEmitter(t *testing.T, src *stubBlockSource, opts BadgeEmitterOptions) *BadgeEmitter {
	t.Helper()
	det := session.NewHitDetector(src)
	if opts.Now == nil {
		opts.Now = time.Now
	}
	return NewBadgeEmitter(det, nil, opts)
}

func TestBadgeEmitter_SkipsEmptyText(t *testing.T) {
	t.Parallel()
	src := newStubSource(session.BlockCandidate{ID: "b1", ExpressionEN: "ship it"})
	em := newTestEmitter(t, src, BadgeEmitterOptions{})
	conn := fakeWSConn{}
	em.Emit(context.Background(), &conn, "u1", "s1", "t1", "   ")
	// Emit is fire-and-forget; give goroutine a moment to run.
	time.Sleep(50 * time.Millisecond)
	if len(conn.written) != 0 {
		t.Fatalf("source called %d times despite empty text", atomic.LoadInt32(&src.calls))
	}
	if got := atomic.LoadInt32(&src.calls); got != 0 {
		t.Fatalf("source called %d times despite empty text", got)
	}
}

func TestBadgeEmitter_RespectsTimeout(t *testing.T) {
	t.Parallel()
	// Stub delays longer than the timeout so Detect cancels before the source
	// returns. The emitter must return nil (miss, no badge written).
	src := withDelay([]session.BlockCandidate{
		{ID: "block-1", ExpressionEN: "ship it"},
	}, 500*time.Millisecond)
	em := newTestEmitter(t, src, BadgeEmitterOptions{Timeout: 100 * time.Millisecond})
	conn := fakeWSConn{}
	// EmitSync runs doEmit synchronously; it returns nil even when Detect times out.
	em.EmitSync(context.Background(), &conn, "u1", "sess-1", "turn-9", "ship it")
	if len(conn.written) != 0 {
		t.Fatalf("no badge on timeout, got %d frames", len(conn.written))
	}
}

func TestBadgeEmitter_DropsDuplicateWithinTTL(t *testing.T) {
	t.Parallel()
	src := newStubSource(session.BlockCandidate{ID: "block-1", ExpressionEN: "ship it"})
	now := time.Now()
	clock := func() time.Time { return now }
	em := newTestEmitter(t, src, BadgeEmitterOptions{Timeout: 500 * time.Millisecond, DedupeTTL: 5 * time.Second, Now: clock})
	conn := fakeWSConn{}
	ctx := context.Background()

	em.EmitSync(ctx, &conn, "u1", "sess-1", "turn-9", "ship it today")
	if len(conn.written) != 1 {
		t.Fatalf("first: written %d want 1", len(conn.written))
	}

	// Second emit with same dedupe key — should be suppressed silently.
	conn.written = nil
	em.EmitSync(ctx, &conn, "u1", "sess-1", "turn-9", "ship it today")
	if len(conn.written) != 0 {
		t.Fatalf("second: written %d want 0 (dedupe suppressed)", len(conn.written))
	}
}

func TestBadgeEmitter_AllowsAfterTTLExpires(t *testing.T) {
	t.Parallel()
	src := newStubSource(session.BlockCandidate{ID: "block-1", ExpressionEN: "ship it"})
	now := time.Unix(1_700_000_000, 0)
	clock := func() time.Time { return now }
	em := newTestEmitter(t, src, BadgeEmitterOptions{Timeout: 500 * time.Millisecond, DedupeTTL: 5 * time.Second, Now: clock})
	conn := fakeWSConn{}
	ctx := context.Background()

	em.EmitSync(ctx, &conn, "u1", "sess-1", "turn-9", "ship it")
	if len(conn.written) != 1 {
		t.Fatalf("first: written %d want 1", len(conn.written))
	}

	// Advance clock past the TTL — second emit should produce a new frame.
	now = now.Add(10 * time.Second)
	conn.written = nil
	em.EmitSync(ctx, &conn, "u1", "sess-1", "turn-9", "ship it")
	if len(conn.written) != 1 {
		t.Fatalf("after TTL: written %d want 1", len(conn.written))
	}
}

func TestBadgeEmitter_PropagatesSourceErrorGracefully(t *testing.T) {
	t.Parallel()
	src := newStubSource()
	src.err = errors.New("db down")
	em := newTestEmitter(t, src, BadgeEmitterOptions{Timeout: 500 * time.Millisecond})
	conn := fakeWSConn{}
	// EmitSync runs doEmit synchronously in the caller's goroutine.
	em.EmitSync(context.Background(), &conn, "u1", "sess-1", "turn-9", "ship it")
	if len(conn.written) != 0 {
		t.Fatalf("no badge on source error, got %d frames", len(conn.written))
	}
}

func TestBadgeEmitter_EmitsFeedbackBadgeOnHit(t *testing.T) {
	t.Parallel()
	src := newStubSource(session.BlockCandidate{
		ID:           "block-1",
		ExpressionEN: "ship it",
		IntentZH:     "推进",
	})
	em := newTestEmitter(t, src, BadgeEmitterOptions{Timeout: 500 * time.Millisecond})

	// Directly invoke run() so we control the connection and can inspect output.
	conn := fakeWSConn{}
	ctx := context.Background()
	em.EmitSync(ctx, &conn, "u1", "sess-1", "turn-9", "ship it today")
	if len(conn.written) != 1 {
		t.Fatalf("written frames: got %d want 1", len(conn.written))
	}
	var badge voiceproto.FeedbackBadge
	if err := json.Unmarshal(conn.written[0], &badge); err != nil {
		t.Fatalf("decode badge: %v", err)
	}
	if badge.Type != voiceproto.TypeFeedbackBadge {
		t.Fatalf("type: got %q", badge.Type)
	}
	if badge.PhraseBlockID != "block-1" {
		t.Fatalf("phrase_block_id: got %q", badge.PhraseBlockID)
	}
	if badge.DedupeKey != "sess-1|turn-9|block-1" {
		t.Fatalf("dedupe_key: got %q", badge.DedupeKey)
	}
	if badge.Tier != voiceproto.BadgeTierSoft {
		t.Fatalf("tier: got %q", badge.Tier)
	}
}

func TestDedupeLRU_AllowIsIdempotent(t *testing.T) {
	t.Parallel()
	lru := newDedupeLRU(8, 5*time.Second, time.Now)
	if lru.Allow("k") {
		t.Fatal("first Allow should be false (fresh key)")
	}
	if !lru.Allow("k") {
		t.Fatal("second Allow should be true (duplicate)")
	}
}

func TestDedupeLRU_EvictsExpiredOnRead(t *testing.T) {
	t.Parallel()
	now := time.Unix(0, 0)
	clock := func() time.Time { return now }
	lru := newDedupeLRU(8, 5*time.Second, clock)
	lru.Allow("k") // store at t=0, expires at t=5
	if lru.Len() != 1 {
		t.Fatalf("len after insert: %d", lru.Len())
	}
	now = now.Add(6 * time.Second)
	if lru.Allow("k") {
		t.Fatal("Allow after expiry should be false")
	}
	if lru.Len() != 1 {
		t.Fatalf("expired entry should have been replaced, len=%d", lru.Len())
	}
}

func TestDedupeLRU_CapacityBound(t *testing.T) {
	t.Parallel()
	lru := newDedupeLRU(2, 5*time.Second, time.Now)
	lru.Allow("a")
	lru.Allow("b")
	lru.Allow("c")
	if got := lru.Len(); got != 2 {
		t.Fatalf("len: got %d want 2", got)
	}
	// The oldest (a) is gone; the new ones (b, c) still dedupe.
	if lru.Allow("b") != true {
		t.Fatal("b should still dedupe")
	}
	if lru.Allow("a") != false {
		t.Fatal("a should be fresh after eviction")
	}
}

func TestDedupeLRU_EmptyKeyAlwaysSuppressed(t *testing.T) {
	t.Parallel()
	lru := newDedupeLRU(8, 5*time.Second, time.Now)
	if !lru.Allow("") {
		t.Fatal("empty key should return true (suppress), caller must not pass empty")
	}
}

// TestBadgeEmitter_StatsTracksOutcomes verifies that the counters exposed via
// Stats() reflect every outcome the emitter can produce: empty skip, miss,
// hit, dedupe suppression, detector error, write error.
func TestBadgeEmitter_StatsTracksOutcomes(t *testing.T) {
	t.Parallel()
	ctx := context.Background()

	t.Run("empty text bumps EmptySkips", func(t *testing.T) {
		t.Parallel()
		src := newStubSource(session.BlockCandidate{ID: "b1", ExpressionEN: "ship it"})
		em := newTestEmitter(t, src, BadgeEmitterOptions{})
		conn := &fakeWSConn{}
		em.Emit(ctx, conn, "u", "s", "t", "   ")
		em.EmitSync(ctx, conn, "u", "s", "t", "")
		if got := em.Stats().EmptySkips; got != 2 {
			t.Fatalf("EmptySkips=%d, want 2", got)
		}
		if got := em.Stats().EmitCalls; got != 0 {
			t.Fatalf("EmitCalls=%d, want 0 (empty text never reached detect)", got)
		}
	})

	t.Run("miss bumps Misses", func(t *testing.T) {
		t.Parallel()
		src := newStubSource(session.BlockCandidate{ID: "b1", ExpressionEN: "ship it"})
		em := newTestEmitter(t, src, BadgeEmitterOptions{})
		conn := &fakeWSConn{}
		em.EmitSync(ctx, conn, "u", "s", "t", "no hit here")
		if got := em.Stats().Misses; got != 1 {
			t.Fatalf("Misses=%d, want 1", got)
		}
		if got := em.Stats().EmitCalls; got != 1 {
			t.Fatalf("EmitCalls=%d, want 1", got)
		}
		if got := em.Stats().Hits; got != 0 {
			t.Fatalf("Hits=%d, want 0", got)
		}
	})

	t.Run("hit bumps Hits", func(t *testing.T) {
		t.Parallel()
		src := newStubSource(session.BlockCandidate{ID: "b1", ExpressionEN: "ship it today"})
		em := newTestEmitter(t, src, BadgeEmitterOptions{})
		conn := &fakeWSConn{}
		em.EmitSync(ctx, conn, "u", "s", "t", "we should ship it today")
		if got := em.Stats().Hits; got != 1 {
			t.Fatalf("Hits=%d, want 1", got)
		}
	})

	t.Run("dedupe bumps DedupDropped on second emit", func(t *testing.T) {
		t.Parallel()
		src := newStubSource(session.BlockCandidate{ID: "b1", ExpressionEN: "ship it today"})
		em := newTestEmitter(t, src, BadgeEmitterOptions{DedupeTTL: 30 * time.Second})
		conn := &fakeWSConn{}
		em.EmitSync(ctx, conn, "u", "s", "t", "we should ship it today")
		em.EmitSync(ctx, conn, "u", "s", "t", "we should ship it today")
		stats := em.Stats()
		if stats.Hits != 1 {
			t.Fatalf("Hits=%d, want 1", stats.Hits)
		}
		if stats.DedupDropped != 1 {
			t.Fatalf("DedupDropped=%d, want 1", stats.DedupDropped)
		}
	})

	t.Run("detector error bumps DetectErrors", func(t *testing.T) {
		t.Parallel()
		src := newStubSource(session.BlockCandidate{ID: "b1", ExpressionEN: "x"})
		src.err = errors.New("db down")
		em := newTestEmitter(t, src, BadgeEmitterOptions{})
		conn := &fakeWSConn{}
		em.EmitSync(ctx, conn, "u", "s", "t", "anything")
		if got := em.Stats().DetectErrors; got != 1 {
			t.Fatalf("DetectErrors=%d, want 1", got)
		}
	})

	t.Run("write error bumps WriteErrors", func(t *testing.T) {
		t.Parallel()
		src := newStubSource(session.BlockCandidate{ID: "b1", ExpressionEN: "ship it today"})
		em := newTestEmitter(t, src, BadgeEmitterOptions{})
		failingConn := &failingBadgeConn{err: errors.New("conn closed")}
		em.EmitSync(ctx, failingConn, "u", "s", "t", "we should ship it today")
		if got := em.Stats().WriteErrors; got != 1 {
			t.Fatalf("WriteErrors=%d, want 1", got)
		}
		if got := em.Stats().Hits; got != 0 {
			t.Fatalf("Hits=%d, want 0 (write failed)", got)
		}
	})
}

// failingBadgeConn is a test double that always returns the configured error.
type failingBadgeConn struct{ err error }

func (f *failingBadgeConn) WriteBadge(_ context.Context, _ []byte) error { return f.err }
