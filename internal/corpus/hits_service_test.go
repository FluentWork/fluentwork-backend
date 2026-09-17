package corpus

import (
	"context"
	"sync"
	"testing"
	"time"
)

func seedBlock(t *testing.T, store *MemoryStore, id, userID, intent, expr string) {
	t.Helper()
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	if _, err := store.SaveAcceptedBlocks(context.Background(), []PhraseBlock{{
		ID:             id,
		UserID:         userID,
		IntentZH:       intent,
		ExpressionEN:   expr,
		AnchorUserSaid: expr,
		SceneTag:       "review",
		FunctionTag:    "commit",
		State:          StateNew,
		NextDueAt:      now,
		EaseFactor:     defaultEase,
		CreatedAt:      now,
		UpdatedAt:      now,
	}}); err != nil {
		t.Fatalf("seed block %s: %v", id, err)
	}
}

func TestHitsService_RecordHits_SingleTurn(t *testing.T) {
	store := NewMemoryStore()
	seedBlock(t, store, "block-1", "user-1", "推动上线", "Let's ship it.")
	svc := NewHitsService(store)

	n, err := svc.RecordHits(context.Background(), "user-1", "session-1", "turn-1", []Hit{
		{BlockID: "block-1", DetectedAtMs: 1_000},
	})
	if err != nil {
		t.Fatalf("RecordHits: %v", err)
	}
	if n != 1 {
		t.Fatalf("recorded_count = %d", n)
	}
	block, err := store.GetBlock(context.Background(), "user-1", "block-1")
	if err != nil {
		t.Fatalf("GetBlock: %v", err)
	}
	if block.TotalUses != 1 {
		t.Fatalf("total_uses = %d", block.TotalUses)
	}
	if block.RealUseCount != 1 {
		t.Fatalf("real_use_count = %d, want 1 (§5.2.3 回写)", block.RealUseCount)
	}
	if block.LastUsedAt == nil || block.LastUsedAt.UnixMilli() != 1_000 {
		t.Fatalf("last_used_at = %v", block.LastUsedAt)
	}
	// 视同一次成功：§5.3.2 首发命中即 new → training，+24h 重排。
	usedAt := time.UnixMilli(1_000).UTC()
	if block.State != StateTraining || block.SuccessStreak != 1 {
		t.Fatalf("state=%s streak=%d, want training/1", block.State, block.SuccessStreak)
	}
	if !block.NextDueAt.Equal(usedAt.Add(24 * time.Hour)) {
		t.Fatalf("next_due_at = %v, want %v", block.NextDueAt, usedAt.Add(24*time.Hour))
	}
}

// §5.3.2 实战命中视同一次成功：连续三个话轮命中应当把块推到绿灯。
func TestHitsService_RecordHits_PromotesToAutomated(t *testing.T) {
	store := NewMemoryStore()
	seedBlock(t, store, "block-1", "user-1", "推动上线", "Let's ship it.")
	svc := NewHitsService(store)

	// Ordered slice, not a map: the last hit decides next_due_at.
	turns := []struct {
		id string
		ms int64
	}{{"turn-1", 1_000}, {"turn-2", 2_000}, {"turn-3", 3_000}}
	for _, turn := range turns {
		if _, err := svc.RecordHits(context.Background(), "user-1", "session-1", turn.id, []Hit{
			{BlockID: "block-1", DetectedAtMs: turn.ms},
		}); err != nil {
			t.Fatalf("%s: %v", turn.id, err)
		}
	}
	block, err := store.GetBlock(context.Background(), "user-1", "block-1")
	if err != nil {
		t.Fatalf("GetBlock: %v", err)
	}
	if block.State != StateAutomated || block.SuccessStreak != 3 {
		t.Fatalf("state=%s streak=%d, want automated/3", block.State, block.SuccessStreak)
	}
	if block.RealUseCount != 3 || block.TotalUses != 3 {
		t.Fatalf("real_use_count=%d total_uses=%d, want 3/3", block.RealUseCount, block.TotalUses)
	}
	usedAt := time.UnixMilli(3_000).UTC()
	if !block.NextDueAt.Equal(usedAt.Add(7 * 24 * time.Hour)) {
		t.Fatalf("next_due_at = %v, want %v", block.NextDueAt, usedAt.Add(7*24*time.Hour))
	}
}

func TestHitsService_RecordHits_CrossTurnAccumulates(t *testing.T) {
	store := NewMemoryStore()
	seedBlock(t, store, "block-1", "user-1", "推动上线", "Let's ship it.")
	svc := NewHitsService(store)

	if _, err := svc.RecordHits(context.Background(), "user-1", "session-1", "turn-1", []Hit{
		{BlockID: "block-1", DetectedAtMs: 1_000},
	}); err != nil {
		t.Fatalf("turn-1: %v", err)
	}
	if _, err := svc.RecordHits(context.Background(), "user-1", "session-1", "turn-2", []Hit{
		{BlockID: "block-1", DetectedAtMs: 2_000},
	}); err != nil {
		t.Fatalf("turn-2: %v", err)
	}
	block, err := store.GetBlock(context.Background(), "user-1", "block-1")
	if err != nil {
		t.Fatalf("GetBlock: %v", err)
	}
	if block.TotalUses != 2 {
		t.Fatalf("total_uses = %d, want 2", block.TotalUses)
	}
	if block.RealUseCount != 2 || block.SuccessStreak != 2 {
		t.Fatalf("real_use_count=%d streak=%d, want 2/2", block.RealUseCount, block.SuccessStreak)
	}
}

func TestHitsService_RecordHits_DuplicateSameTurnDoesNotDoubleCount(t *testing.T) {
	store := NewMemoryStore()
	seedBlock(t, store, "block-1", "user-1", "推动上线", "Let's ship it.")
	svc := NewHitsService(store)
	hit := []Hit{{BlockID: "block-1", DetectedAtMs: 1_000}}

	if _, err := svc.RecordHits(context.Background(), "user-1", "session-1", "turn-1", hit); err != nil {
		t.Fatalf("first: %v", err)
	}
	if _, err := svc.RecordHits(context.Background(), "user-1", "session-1", "turn-1", []Hit{
		{BlockID: "block-1", DetectedAtMs: 1_500},
	}); err != nil {
		t.Fatalf("duplicate: %v", err)
	}
	block, err := store.GetBlock(context.Background(), "user-1", "block-1")
	if err != nil {
		t.Fatalf("GetBlock: %v", err)
	}
	if block.TotalUses != 1 {
		t.Fatalf("total_uses = %d, want 1 after duplicate UPSERT", block.TotalUses)
	}
	// 幂等：同一 (session, turn, block) 的重复上报不得二次回写。
	if block.RealUseCount != 1 || block.SuccessStreak != 1 {
		t.Fatalf("real_use_count=%d streak=%d, want 1/1", block.RealUseCount, block.SuccessStreak)
	}
	hits, err := store.ListSessionHits(context.Background(), "session-1")
	if err != nil {
		t.Fatalf("ListSessionHits: %v", err)
	}
	if len(hits) != 1 || hits[0].UsedAtMs != 1_500 {
		t.Fatalf("ledger = %+v", hits)
	}
}

func TestHitsService_RecordHits_ConcurrentSameHit(t *testing.T) {
	store := NewMemoryStore()
	seedBlock(t, store, "block-1", "user-1", "推动上线", "Let's ship it.")
	svc := NewHitsService(store)

	const n = 100
	var wg sync.WaitGroup
	errs := make(chan error, n)
	wg.Add(n)
	for i := 0; i < n; i++ {
		go func() {
			defer wg.Done()
			_, err := svc.RecordHits(context.Background(), "user-1", "session-1", "turn-1", []Hit{
				{BlockID: "block-1", DetectedAtMs: 9_000},
			})
			if err != nil {
				errs <- err
			}
		}()
	}
	wg.Wait()
	close(errs)
	for err := range errs {
		t.Fatalf("RecordHits: %v", err)
	}
	block, err := store.GetBlock(context.Background(), "user-1", "block-1")
	if err != nil {
		t.Fatalf("GetBlock: %v", err)
	}
	if block.TotalUses != 1 {
		drift := float64(block.TotalUses-1) / 1.0
		if drift > 0.01 {
			t.Fatalf("total_uses = %d, concurrent drift %f > 1%%", block.TotalUses, drift)
		}
	}
}

func TestHitsService_RecentHits_LookbackAndTTL(t *testing.T) {
	store := NewMemoryStore()
	seedBlock(t, store, "block-1", "user-1", "推动上线", "Let's ship it.")
	seedBlock(t, store, "block-2", "user-1", "说明阻塞", "I'm blocked.")
	svc := NewHitsService(store)

	for i, turn := range []string{"turn-1", "turn-2", "turn-3"} {
		blockID := "block-1"
		if i == 2 {
			blockID = "block-2"
		}
		if _, err := svc.RecordHits(context.Background(), "user-1", "session-1", turn, []Hit{
			{BlockID: blockID, DetectedAtMs: int64((i + 1) * 1000)},
		}); err != nil {
			t.Fatalf("record %s: %v", turn, err)
		}
	}

	all, err := svc.RecentHits(context.Background(), "session-1", DefaultLookbackTurns, DefaultMinScore)
	if err != nil {
		t.Fatalf("RecentHits: %v", err)
	}
	if len(all.Hits) != 3 {
		t.Fatalf("hits = %+v", all.Hits)
	}
	if all.TTLAtMs != 3_000+RecentHitTTLMs {
		t.Fatalf("ttl_at_ms = %d", all.TTLAtMs)
	}
	if all.Hits[0].ChunkEN != "I'm blocked." || all.Hits[0].IntentZH != "说明阻塞" {
		t.Fatalf("newest hit = %+v", all.Hits[0])
	}

	one, err := svc.RecentHits(context.Background(), "session-1", 1, DefaultMinScore)
	if err != nil {
		t.Fatalf("lookback 1: %v", err)
	}
	if len(one.Hits) != 1 || one.Hits[0].TurnID != "turn-3" {
		t.Fatalf("lookback 1 = %+v", one.Hits)
	}

	zero, err := svc.RecentHits(context.Background(), "session-1", 0, DefaultMinScore)
	if err != nil {
		t.Fatalf("lookback 0: %v", err)
	}
	if len(zero.Hits) != 0 || zero.TTLAtMs != 0 {
		t.Fatalf("lookback 0 = %+v ttl=%d", zero.Hits, zero.TTLAtMs)
	}
}

func TestHitsService_RecentHits_ExcludesDeletedBlocks(t *testing.T) {
	store := NewMemoryStore()
	seedBlock(t, store, "block-1", "user-1", "推动上线", "Let's ship it.")
	svc := NewHitsService(store)
	if _, err := svc.RecordHits(context.Background(), "user-1", "session-1", "turn-1", []Hit{
		{BlockID: "block-1", DetectedAtMs: 1_000},
	}); err != nil {
		t.Fatalf("RecordHits: %v", err)
	}
	if err := store.SoftDeleteBlock(context.Background(), "user-1", "block-1", time.Now().UTC()); err != nil {
		t.Fatalf("SoftDeleteBlock: %v", err)
	}
	got, err := svc.RecentHits(context.Background(), "session-1", 8, DefaultMinScore)
	if err != nil {
		t.Fatalf("RecentHits: %v", err)
	}
	if len(got.Hits) != 0 {
		t.Fatalf("deleted block leaked: %+v", got.Hits)
	}
}

func TestHitsService_RecordHits_RequiresIDs(t *testing.T) {
	svc := NewHitsService(NewMemoryStore())
	if _, err := svc.RecordHits(context.Background(), "", "s", "t", nil); err == nil {
		t.Fatal("expected user_id error")
	}
	if _, err := svc.RecordHits(context.Background(), "u", "", "t", nil); err == nil {
		t.Fatal("expected session_id error")
	}
	if _, err := svc.RecordHits(context.Background(), "u", "s", "", nil); err == nil {
		t.Fatal("expected turn_id error")
	}
}
