package corpus

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/apierr"
)

func realUseFixture(t *testing.T) (*Service, *MemoryStore, time.Time) {
	t.Helper()
	store := NewMemoryStore()
	svc := NewService(store, nil)
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }
	return svc, store, now
}

// A checkin-confirmed use is credited like a B7 hit: the counter moves and the
// recall ladder advances (PRD §5.2.3's 视同成功).
func TestRecordRealUse_CreditsAndReschedules(t *testing.T) {
	svc, store, now := realUseFixture(t)
	seedRecommendBlock(t, store, "b1", "standup", "report", StateTraining, 0, nil, now)
	if _, err := store.UpdateSchedule(context.Background(), "user-1", "b1", StateTraining, 1, now, now); err != nil {
		t.Fatalf("seed streak: %v", err)
	}

	credited, err := svc.RecordRealUse(context.Background(), "user-1", []string{"b1"}, RealUseSourceCheckin, "card-1")
	if err != nil {
		t.Fatalf("RecordRealUse: %v", err)
	}
	if credited != 1 {
		t.Fatalf("credited = %d, want 1", credited)
	}
	block, err := store.GetBlock(context.Background(), "user-1", "b1")
	if err != nil {
		t.Fatalf("GetBlock: %v", err)
	}
	if block.RealUseCount != 1 || block.TotalUses != 1 {
		t.Fatalf("counters = %d/%d", block.RealUseCount, block.TotalUses)
	}
	if block.SuccessStreak != 2 {
		t.Fatalf("streak = %d, want the hit ladder's 2", block.SuccessStreak)
	}
	if block.LastUsedAt == nil {
		t.Fatal("last_used_at must move")
	}
}

// The same card cannot credit the same phrase twice, however many times the
// client retries the checkin.
func TestRecordRealUse_IdempotentPerRef(t *testing.T) {
	svc, store, now := realUseFixture(t)
	seedRecommendBlock(t, store, "b1", "standup", "report", StateTraining, 0, nil, now)

	for i := 0; i < 3; i++ {
		if _, err := svc.RecordRealUse(context.Background(), "user-1", []string{"b1"}, RealUseSourceCheckin, "card-1"); err != nil {
			t.Fatalf("call %d: %v", i, err)
		}
	}
	block, err := store.GetBlock(context.Background(), "user-1", "b1")
	if err != nil {
		t.Fatalf("GetBlock: %v", err)
	}
	if block.RealUseCount != 1 {
		t.Fatalf("real_use_count = %d, want one credit", block.RealUseCount)
	}

	// A different card is a different real conversation.
	if _, err := svc.RecordRealUse(context.Background(), "user-1", []string{"b1"}, RealUseSourceCheckin, "card-2"); err != nil {
		t.Fatalf("second card: %v", err)
	}
	block, _ = store.GetBlock(context.Background(), "user-1", "b1")
	if block.RealUseCount != 2 {
		t.Fatalf("real_use_count = %d, want 2", block.RealUseCount)
	}
}

// The L1/L2 split (86_ M9) only works if both paths record provenance.
func TestCountRealUsesBySource_SplitsObservationFromReport(t *testing.T) {
	svc, store, now := realUseFixture(t)
	seedRecommendBlock(t, store, "b1", "standup", "report", StateTraining, 0, nil, now)
	seedRecommendBlock(t, store, "b2", "standup", "report", StateTraining, 0, nil, now)

	if _, err := NewHitsService(store).RecordHits(context.Background(), "user-1", "session-1", "turn-1",
		[]Hit{{BlockID: "b1", DetectedAtMs: now.UnixMilli()}}); err != nil {
		t.Fatalf("RecordHits: %v", err)
	}
	if _, err := svc.RecordRealUse(context.Background(), "user-1", []string{"b2"}, RealUseSourceCheckin, "card-9"); err != nil {
		t.Fatalf("RecordRealUse: %v", err)
	}

	counts, err := svc.CountRealUsesBySource(context.Background(), "user-1", now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("CountRealUsesBySource: %v", err)
	}
	if counts[RealUseSourceHit] != 1 || counts[RealUseSourceCheckin] != 1 {
		t.Fatalf("counts = %+v, want one of each", counts)
	}
	// Outside the window nothing is counted: this drives a 30-day stat.
	empty, err := svc.CountRealUsesBySource(context.Background(), "user-1", now.Add(time.Hour))
	if err != nil {
		t.Fatalf("CountRealUsesBySource: %v", err)
	}
	if len(empty) != 0 {
		t.Fatalf("window not applied: %+v", empty)
	}
}

func TestRecordRealUse_RejectsBadInputAndForeignBlocks(t *testing.T) {
	svc, store, now := realUseFixture(t)
	seedRecommendBlock(t, store, "b1", "standup", "report", StateTraining, 0, nil, now)
	seedRecommendBlock(t, store, "gone", "standup", "report", StateTraining, 0, nil, now)
	if _, err := store.SoftDeleteAllForUser(context.Background(), "user-1", now); err != nil {
		t.Fatalf("wipe: %v", err)
	}

	var apiErr *apierr.Error
	for name, call := range map[string]func() error{
		"missing user": func() error {
			_, err := svc.RecordRealUse(context.Background(), " ", []string{"b1"}, RealUseSourceCheckin, "c")
			return err
		},
		"unknown source": func() error {
			_, err := svc.RecordRealUse(context.Background(), "user-1", []string{"b1"}, "vibes", "c")
			return err
		},
		"missing ref": func() error {
			_, err := svc.RecordRealUse(context.Background(), "user-1", []string{"b1"}, RealUseSourceCheckin, " ")
			return err
		},
		"too many": func() error {
			ids := make([]string, realUseMaxBlocks+1)
			for i := range ids {
				ids[i] = "b" // non-empty, or the empty-string filter would drop them first
			}
			_, err := svc.RecordRealUse(context.Background(), "user-1", ids, RealUseSourceCheckin, "c")
			return err
		},
	} {
		if err := call(); !errors.As(err, &apiErr) {
			t.Fatalf("%s: err = %v, want an api error", name, err)
		}
	}

	// A deleted block earns no credit, and neither does an unknown one.
	credited, err := svc.RecordRealUse(context.Background(), "user-1", []string{"gone", "nope"}, RealUseSourceCheckin, "card-1")
	if err != nil {
		t.Fatalf("RecordRealUse: %v", err)
	}
	if credited != 0 {
		t.Fatalf("credited = %d, want 0", credited)
	}
}
