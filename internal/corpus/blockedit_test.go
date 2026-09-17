package corpus

import (
	"context"
	"testing"
	"time"
)

func editFixture(t *testing.T) (*Service, *MemoryStore, time.Time) {
	t.Helper()
	store := NewMemoryStore()
	svc := NewService(store, nil)
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }
	seedRecommendBlock(t, store, "b1", "standup", "report", StateTraining, 0, nil, now.Add(-time.Hour))
	// A block with history: recalled twice, due tomorrow.
	if _, err := store.UpdateSchedule(context.Background(), "user-1", "b1", StateTraining, 2, now.Add(24*time.Hour), now); err != nil {
		t.Fatalf("seed streak: %v", err)
	}
	return svc, store, now
}

// 86_ M6: expression_en is what the learner must produce. Rewriting it makes
// this a different sentence to recall, so the evidence goes with the old wording.
func TestUpdateBlock_ExpressionChangeResetsAndVersions(t *testing.T) {
	svc, store, now := editFixture(t)

	view, err := svc.UpdateBlock(context.Background(), "user-1", "b1", UpdateBlockRequest{
		IntentZH:       "说明部署卡住",
		ExpressionEN:   "The deploy is blocked on the migration.",
		AnchorUserSaid: "the deploy is waiting",
		SceneTag:       "standup",
		FunctionTag:    "report",
	})
	if err != nil {
		t.Fatalf("UpdateBlock: %v", err)
	}
	if view.ExpressionVersion != 2 {
		t.Fatalf("version = %d, want 2 after one rewrite", view.ExpressionVersion)
	}
	if view.State != StateNew || view.SuccessStreak != 0 {
		t.Fatalf("schedule not reset: state=%s streak=%d", view.State, view.SuccessStreak)
	}
	// The fresh interval comes from the store's ladder, not a hardcoded 24h.
	if !view.NextDueAt.Equal(now.Add(DefaultSchedule().TrainingInterval)) {
		t.Fatalf("next_due_at = %v, want the ladder's training interval", view.NextDueAt)
	}

	// The history is auditable.
	block, err := store.GetBlock(context.Background(), "user-1", "b1")
	if err != nil {
		t.Fatalf("GetBlock: %v", err)
	}
	if block.ExpressionVersion != 2 {
		t.Fatalf("stored version = %d", block.ExpressionVersion)
	}
	if len(store.Edits()) != 1 {
		t.Fatalf("edits = %+v", store.Edits())
	}
	edit := store.Edits()[0]
	if edit.Version != 2 || edit.OldExpression != "expression b1" || edit.NewExpression != block.ExpressionEN {
		t.Fatalf("edit row = %+v", edit)
	}
}

// Editing the cue or the provenance is not editing the sentence: no reset, no
// new version.
func TestUpdateBlock_NonExpressionEditKeepsTheSchedule(t *testing.T) {
	svc, store, _ := editFixture(t)
	before, err := store.GetBlock(context.Background(), "user-1", "b1")
	if err != nil {
		t.Fatalf("GetBlock: %v", err)
	}

	view, err := svc.UpdateBlock(context.Background(), "user-1", "b1", UpdateBlockRequest{
		IntentZH:       "换个说法描述意图",
		ExpressionEN:   before.ExpressionEN,
		AnchorUserSaid: "另一个锚点",
		SceneTag:       "review",
		FunctionTag:    "commit",
	})
	if err != nil {
		t.Fatalf("UpdateBlock: %v", err)
	}
	if view.ExpressionVersion != 1 || view.SuccessStreak != before.SuccessStreak {
		t.Fatalf("schedule or version moved: %+v", view)
	}
	if !view.NextDueAt.Equal(before.NextDueAt) {
		t.Fatalf("next_due_at moved: %v → %v", before.NextDueAt, view.NextDueAt)
	}
	if len(store.Edits()) != 0 {
		t.Fatalf("an edit that did not touch the expression must not be recorded: %+v", store.Edits())
	}
}

// A deployment that shortened its ladder gets the short interval on reset too.
func TestUpdateBlock_ResetUsesTheConfiguredLadder(t *testing.T) {
	svc, store, now := editFixture(t)
	store.SetSchedule(Schedule{TrainingInterval: 5 * time.Minute, PromoteStreak: 1})

	view, err := svc.UpdateBlock(context.Background(), "user-1", "b1", UpdateBlockRequest{
		IntentZH:       "说明部署卡住",
		ExpressionEN:   "The deploy is blocked on the migration.",
		AnchorUserSaid: "the deploy is waiting",
		SceneTag:       "standup",
		FunctionTag:    "report",
	})
	if err != nil {
		t.Fatalf("UpdateBlock: %v", err)
	}
	if !view.NextDueAt.Equal(now.Add(5 * time.Minute)) {
		t.Fatalf("next_due_at = %v, want the configured 5m", view.NextDueAt)
	}
}
