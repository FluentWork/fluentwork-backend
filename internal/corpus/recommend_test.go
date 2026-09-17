package corpus

import (
	"context"
	"testing"
	"time"
)

func seedRecommendBlock(t *testing.T, store *MemoryStore, id, scene, function, state string, realUses int, lastUsed *time.Time, updated time.Time) {
	t.Helper()
	if _, err := store.SaveAcceptedBlocks(context.Background(), []PhraseBlock{{
		ID:             id,
		UserID:         "user-1",
		IntentZH:       "意图 " + id,
		ExpressionEN:   "expression " + id,
		AnchorUserSaid: "anchor " + id,
		SceneTag:       scene,
		FunctionTag:    function,
		State:          state,
		NextDueAt:      updated,
		EaseFactor:     defaultEase,
		RealUseCount:   realUses,
		LastUsedAt:     lastUsed,
		CreatedAt:      updated,
		UpdatedAt:      updated,
	}}); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

func recommendService(t *testing.T) (*Service, *MemoryStore, time.Time) {
	t.Helper()
	store := NewMemoryStore()
	svc := NewService(store, nil)
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }
	return svc, store, now
}

// Scene match comes first: a session about standup should suggest standup blocks.
func TestRecommend_SceneFirst(t *testing.T) {
	svc, store, now := recommendService(t)
	seedRecommendBlock(t, store, "review-1", "review", "propose", StateTraining, 9, nil, now.Add(-time.Hour))
	seedRecommendBlock(t, store, "standup-1", "standup", "report", StateTraining, 0, nil, now.Add(-time.Hour))

	got, err := svc.Recommend(context.Background(), RecommendRequest{UserID: "user-1", SceneTag: "standup"})
	if err != nil {
		t.Fatalf("Recommend: %v", err)
	}
	if len(got.Items) != 2 {
		t.Fatalf("items = %+v", got.Items)
	}
	if got.Items[0].ID != "standup-1" || got.Items[0].Reason != ReasonSceneMatch {
		t.Fatalf("first item = %+v", got.Items[0])
	}
	// The other scene still fills the list, labelled for what it is: a block the
	// learner actually uses.
	if got.Items[1].ID != "review-1" || got.Items[1].Reason != ReasonMostUsed {
		t.Fatalf("second item = %+v", got.Items[1])
	}
}

// Within a scene, what the learner really uses comes first.
func TestRecommend_MostUsedFirst(t *testing.T) {
	svc, store, now := recommendService(t)
	seedRecommendBlock(t, store, "used-0", "standup", "report", StateTraining, 0, nil, now.Add(-time.Hour))
	seedRecommendBlock(t, store, "used-5", "standup", "report", StateTraining, 5, nil, now.Add(-time.Hour))
	seedRecommendBlock(t, store, "green-1", "standup", "report", StateAutomated, 1, nil, now.Add(-time.Hour))

	got, err := svc.Recommend(context.Background(), RecommendRequest{UserID: "user-1", SceneTag: "standup"})
	if err != nil {
		t.Fatalf("Recommend: %v", err)
	}
	order := []string{got.Items[0].ID, got.Items[1].ID, got.Items[2].ID}
	want := []string{"used-5", "green-1", "used-0"}
	for i := range want {
		if order[i] != want[i] {
			t.Fatalf("order = %v, want %v", order, want)
		}
	}
}

// 已自动化是低接触不是免复习: a green block nobody has touched in a month is
// surfaced as a review prompt, and says so.
func TestRecommend_StaleGreenFlagged(t *testing.T) {
	svc, store, now := recommendService(t)
	stale := now.Add(-45 * 24 * time.Hour)
	seedRecommendBlock(t, store, "green-stale", "standup", "report", StateAutomated, 4, &stale, stale)
	seedRecommendBlock(t, store, "green-fresh", "standup", "report", StateAutomated, 1, nil, now.Add(-time.Hour))

	got, err := svc.Recommend(context.Background(), RecommendRequest{UserID: "user-1", SceneTag: "standup"})
	if err != nil {
		t.Fatalf("Recommend: %v", err)
	}
	staleItem := findItem(t, got.Items, "green-stale")
	if !staleItem.Stale || staleItem.Reason != ReasonStaleGreen {
		t.Fatalf("stale item = %+v", staleItem)
	}
	freshItem := findItem(t, got.Items, "green-fresh")
	if freshItem.Stale {
		t.Fatalf("a recently touched green block is not stale: %+v", freshItem)
	}
}

// A training block is never stale: it is already scheduled.
func TestRecommend_TrainingNeverStale(t *testing.T) {
	svc, store, now := recommendService(t)
	old := now.Add(-90 * 24 * time.Hour)
	seedRecommendBlock(t, store, "training-old", "standup", "report", StateTraining, 0, &old, old)

	got, err := svc.Recommend(context.Background(), RecommendRequest{UserID: "user-1", SceneTag: "standup"})
	if err != nil {
		t.Fatalf("Recommend: %v", err)
	}
	if got.Items[0].Stale {
		t.Fatalf("training block flagged stale: %+v", got.Items[0])
	}
}

func TestRecommend_LimitAndFunctionFilter(t *testing.T) {
	svc, store, now := recommendService(t)
	seedRecommendBlock(t, store, "a", "standup", "report", StateTraining, 1, nil, now.Add(-time.Hour))
	seedRecommendBlock(t, store, "b", "standup", "report", StateTraining, 2, nil, now.Add(-time.Hour))
	seedRecommendBlock(t, store, "c", "standup", "propose", StateTraining, 3, nil, now.Add(-time.Hour))

	got, err := svc.Recommend(context.Background(), RecommendRequest{UserID: "user-1", SceneTag: "standup", Limit: 1})
	if err != nil {
		t.Fatalf("Recommend: %v", err)
	}
	if len(got.Items) != 1 || got.Items[0].ID != "c" {
		t.Fatalf("limit ignored: %+v", got.Items)
	}
	got, err = svc.Recommend(context.Background(), RecommendRequest{UserID: "user-1", FunctionTag: "report"})
	if err != nil {
		t.Fatalf("Recommend: %v", err)
	}
	if len(got.Items) != 2 {
		t.Fatalf("function filter ignored: %+v", got.Items)
	}
}

func TestRecommend_RequiresUser(t *testing.T) {
	svc, _, _ := recommendService(t)
	if _, err := svc.Recommend(context.Background(), RecommendRequest{}); err == nil {
		t.Fatal("missing user must be rejected")
	}
}

func findItem(t *testing.T, items []Recommendation, id string) Recommendation {
	t.Helper()
	for _, item := range items {
		if item.ID == id {
			return item
		}
	}
	t.Fatalf("item %s not in %+v", id, items)
	return Recommendation{}
}

// SweepOverdue is the other half of "过期任务不累积": it is the write the
// recommendation and round logic rely on to keep the overdue set honest.
func TestSweepOverdue_MemoryStore(t *testing.T) {
	store := NewMemoryStore()
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	ancient := now.Add(-30 * 24 * time.Hour)
	recent := now.Add(-2 * time.Hour)
	seedRecommendBlock(t, store, "ancient", "standup", "report", StateTraining, 0, nil, ancient)
	seedRecommendBlock(t, store, "recent", "standup", "report", StateTraining, 0, nil, recent)

	moved, err := store.SweepOverdue(context.Background(), "user-1", now.Add(-72*time.Hour), now)
	if err != nil {
		t.Fatalf("SweepOverdue: %v", err)
	}
	if moved != 1 {
		t.Fatalf("moved = %d, want only the ancient one", moved)
	}
	got, err := store.GetBlock(context.Background(), "user-1", "ancient")
	if err != nil {
		t.Fatalf("GetBlock: %v", err)
	}
	if !got.NextDueAt.Equal(now) {
		t.Fatalf("next_due_at = %v, want the sweep time", got.NextDueAt)
	}
	kept, err := store.GetBlock(context.Background(), "user-1", "recent")
	if err != nil {
		t.Fatalf("GetBlock: %v", err)
	}
	if !kept.NextDueAt.Equal(recent) {
		t.Fatalf("recent block moved: %v", kept.NextDueAt)
	}
}

func TestSweepOverdue_SkipsDeletedAndOtherUsers(t *testing.T) {
	store := NewMemoryStore()
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	ancient := now.Add(-30 * 24 * time.Hour)
	seedRecommendBlock(t, store, "mine", "standup", "report", StateTraining, 0, nil, ancient)
	if _, err := store.SoftDeleteAllForUser(context.Background(), "user-1", now); err != nil {
		t.Fatalf("wipe: %v", err)
	}
	moved, err := store.SweepOverdue(context.Background(), "user-1", now.Add(-72*time.Hour), now)
	if err != nil {
		t.Fatalf("SweepOverdue: %v", err)
	}
	if moved != 0 {
		t.Fatalf("a wiped block must not be swept, moved = %d", moved)
	}
}
