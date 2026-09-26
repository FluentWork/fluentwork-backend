package materials

import (
	"context"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/corpus"
)

func seedStranded(t *testing.T, store *MemoryStore, id string, at time.Time) {
	t.Helper()
	m := Material{
		ID:           id,
		UserID:       "u1",
		Kind:         KindPaste,
		Content:      "standup notes",
		RefineStatus: StatusQueued,
		CreatedAt:    at,
		UpdatedAt:    at,
	}
	if err := store.InsertMaterial(context.Background(), m); err != nil {
		t.Fatalf("insert %s: %v", id, err)
	}
	if err := store.MarkProcessing(context.Background(), id, at); err != nil {
		t.Fatalf("claim %s: %v", id, err)
	}
}

func statusOf(t *testing.T, store *MemoryStore, id string) Material {
	t.Helper()
	got, err := store.GetMaterial(context.Background(), "u1", id)
	if err != nil {
		t.Fatalf("get %s: %v", id, err)
	}
	return got
}

func transitionCount(from, to string) int64 {
	transMu.Lock()
	defer transMu.Unlock()
	return transitions[from+"->"+to]
}

func TestMemoryStore_ReclaimExpiredLeavesALiveLeaseAlone(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	claimedAt := time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC)
	seedStranded(t, store, "m-live", claimedAt)

	result, err := store.ReclaimExpired(ctx, claimedAt.Add(DefaultRefineLease-time.Second))
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if len(result.Requeued) != 0 || result.Expired != 0 {
		t.Fatalf("a lease with a second left was swept: %+v", result)
	}
	if got := statusOf(t, store, "m-live"); got.RefineStatus != StatusProcessing {
		t.Fatalf("status = %s, want the live claim left alone", got.RefineStatus)
	}
}

func TestMemoryStore_ReclaimExpiredRequeuesAnAbandonedRefine(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	claimedAt := time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC)
	seedStranded(t, store, "m-stuck", claimedAt)

	result, err := store.ReclaimExpired(ctx, claimedAt.Add(DefaultRefineLease+time.Second))
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if len(result.Requeued) != 1 || result.Requeued[0] != "m-stuck" {
		t.Fatalf("requeued = %v, want [m-stuck]", result.Requeued)
	}
	if result.Expired != 0 {
		t.Fatalf("expired = %d, want 0 while attempts remain", result.Expired)
	}
	if got := statusOf(t, store, "m-stuck"); got.RefineStatus != StatusQueued {
		t.Fatalf("status = %s, want queued so it can be claimed again", got.RefineStatus)
	}

	again := claimedAt.Add(DefaultRefineLease + 2*time.Second)
	if err := store.MarkProcessing(ctx, "m-stuck", again); err != nil {
		t.Fatalf("claim after reclaim: %v", err)
	}
	if got := statusOf(t, store, "m-stuck"); got.attempts != 2 {
		t.Fatalf("attempts = %d, want 2 after a reclaim and a second claim", got.attempts)
	}
}

func TestMemoryStore_ReclaimExpiredFailsARefineThatRanOutOfAttempts(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	start := time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC)
	seedStranded(t, store, "m-doomed", start)

	at := start
	for attempt := 1; attempt <= MaxRefineAttempts; attempt++ {
		at = at.Add(DefaultRefineLease + time.Second)
		result, err := store.ReclaimExpired(ctx, at)
		if err != nil {
			t.Fatalf("reclaim %d: %v", attempt, err)
		}
		if attempt < MaxRefineAttempts {
			if len(result.Requeued) != 1 || result.Expired != 0 {
				t.Fatalf("reclaim %d = %+v, want one requeue", attempt, result)
			}
			if err := store.MarkProcessing(ctx, "m-doomed", at.Add(time.Second)); err != nil {
				t.Fatalf("claim %d: %v", attempt+1, err)
			}
			continue
		}
		if len(result.Requeued) != 0 || result.Expired != 1 {
			t.Fatalf("last reclaim = %+v, want the row failed rather than requeued", result)
		}
	}

	got := statusOf(t, store, "m-doomed")
	if got.RefineStatus != StatusFailed || got.ErrorCode != ErrorLeaseExpired {
		t.Fatalf("got %s/%s, want failed/%s — otherwise it is a spinner with no exit",
			got.RefineStatus, got.ErrorCode, ErrorLeaseExpired)
	}
}

func TestMemoryStore_ReclaimExpiredSkipsADeletedMaterial(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	at := time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC)
	seedStranded(t, store, "m-gone", at)
	if _, err := store.SoftDeleteAllForUser(ctx, "u1", at); err != nil {
		t.Fatalf("soft delete: %v", err)
	}

	result, err := store.ReclaimExpired(ctx, at.Add(DefaultRefineLease+time.Second))
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if len(result.Requeued) != 0 || result.Expired != 0 {
		t.Fatalf("a deleted material was swept: %+v", result)
	}
}

func TestService_ReclaimExpiredRefinesTheMaterialItTookBack(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	blocks := corpus.NewMemoryStore()
	svc := NewService(store, blocks, stubLLM{body: fiveBlockJSON()}, nil)

	start := time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC)
	seedStranded(t, store, "m-stuck", start)
	svc.now = func() time.Time { return start.Add(DefaultRefineLease + time.Second) }

	requeued, err := svc.ReclaimExpired(ctx)
	if err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if requeued != 1 {
		t.Fatalf("requeued = %d, want 1", requeued)
	}

	deadline := time.Now().Add(5 * time.Second)
	for {
		got := statusOf(t, store, "m-stuck")
		if got.RefineStatus == StatusFailed {
			t.Fatalf("the reclaimed refine failed: %s", got.ErrorCode)
		}
		if got.RefineStatus == StatusReady {
			if got.BlockCount != 5 {
				t.Fatalf("block_count = %d, want 5", got.BlockCount)
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("the reclaimed material stayed %s", got.RefineStatus)
		}
		time.Sleep(2 * time.Millisecond)
	}

	listed, err := blocks.ListBlocks(ctx, corpus.ListFilter{UserID: "u1", Limit: 20})
	if err != nil || len(listed) != 5 {
		t.Fatalf("blocks n=%d err=%v", len(listed), err)
	}
}

type countingStore struct {
	*MemoryStore
	mu       sync.Mutex
	reclaims int
}

func (s *countingStore) ReclaimExpired(ctx context.Context, at time.Time) (ReclaimResult, error) {
	s.mu.Lock()
	s.reclaims++
	s.mu.Unlock()
	return s.MemoryStore.ReclaimExpired(ctx, at)
}

func (s *countingStore) reclaimCalls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.reclaims
}

func TestService_SweepIfDueSweepsOncePerInterval(t *testing.T) {
	ctx := context.Background()
	store := &countingStore{MemoryStore: NewMemoryStore()}
	svc := NewService(store, corpus.NewMemoryStore(), stubLLM{body: fiveBlockJSON()}, nil)

	at := time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC)
	svc.SweepIfDue(ctx, at)
	svc.SweepIfDue(ctx, at.Add(time.Second))
	svc.SweepIfDue(ctx, at.Add(ReclaimInterval-time.Second))
	if got := store.reclaimCalls(); got != 1 {
		t.Fatalf("swept %d times inside one interval, want 1", got)
	}

	svc.SweepIfDue(ctx, at.Add(ReclaimInterval))
	if got := store.reclaimCalls(); got != 2 {
		t.Fatalf("swept %d times, want a second sweep once the interval elapsed", got)
	}
}

func TestService_ReclaimExpiredRecordsTheRequeueAsATransition(t *testing.T) {
	ctx := context.Background()
	store := NewMemoryStore()
	svc := NewService(store, corpus.NewMemoryStore(), stubLLM{body: fiveBlockJSON()}, nil)

	start := time.Date(2026, 9, 26, 10, 0, 0, 0, time.UTC)
	seedStranded(t, store, "m-stuck", start)
	svc.now = func() time.Time { return start.Add(DefaultRefineLease + time.Second) }

	before := transitionCount(StatusProcessing, StatusQueued)
	if _, err := svc.ReclaimExpired(ctx); err != nil {
		t.Fatalf("reclaim: %v", err)
	}
	if got := transitionCount(StatusProcessing, StatusQueued) - before; got != 1 {
		t.Fatalf("processing->queued counted %d times, want 1", got)
	}
	if !strings.Contains(PrometheusMetrics(), `refine_status_transition_total{from="processing",to="queued"}`) {
		t.Fatal("the reclaim left no trace on /metrics")
	}
}
