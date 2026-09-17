package drill

import (
	"context"
	"testing"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/corpus"
)

// seedTagged seeds one block with a chosen function tag.
func seedTagged(t *testing.T, store *corpus.MemoryStore, userID, id, functionTag string, due time.Time) {
	t.Helper()
	if _, err := store.SaveAcceptedBlocks(context.Background(), []corpus.PhraseBlock{{
		ID: id, UserID: userID, IntentZH: "意图", ExpressionEN: "Let's ship it " + id,
		AnchorUserSaid: "ship " + id, SceneTag: "review", FunctionTag: functionTag,
		State: corpus.StateTraining, NextDueAt: due, EaseFactor: 2.5,
		CreatedAt: due.Add(-time.Hour), UpdatedAt: due.Add(-time.Hour),
	}}); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

type stubRescues struct {
	counts map[string]int
	err    error
}

func (s stubRescues) CountRescuesByPath(context.Context, string, time.Time) (map[string]int, error) {
	return s.counts, s.err
}

// 86_ M4: the map answers "which kinds of things does this learner keep failing
// to say" — the unit is the intent type, not the phrase.
func TestStuckMap_GroupsByFunctionTag(t *testing.T) {
	svc, blocks, recs, now := configFixture(t)
	seedTagged(t, blocks, "user-1", "report-1", "report", now.Add(-time.Hour))
	seedTagged(t, blocks, "user-1", "clarify-1", "commit", now.Add(-time.Hour))

	// One forgotten-after-learning failure (streak was 2) and one fresh failure.
	if _, err := recs.Insert(context.Background(), Record{
		UserID: "user-1", BlockID: "report-1", Judged: true, SemanticPass: false,
		PrevState: corpus.StateTraining, PrevSuccessStreak: 2, CreatedAt: now.Add(-time.Hour),
	}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if _, err := recs.Insert(context.Background(), Record{
		UserID: "user-1", BlockID: "clarify-1", Judged: true, SemanticPass: false,
		PrevState: corpus.StateTraining, PrevSuccessStreak: 0, CreatedAt: now.Add(-30 * time.Minute),
	}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if _, err := recs.Insert(context.Background(), Record{
		UserID: "user-1", BlockID: "report-1", Judged: true, SemanticPass: true,
		PrevState: corpus.StateNew, PrevSuccessStreak: 0, CreatedAt: now.Add(-10 * time.Minute),
	}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	// An unjudged attempt says nothing about recall and must not appear.
	if _, err := recs.Insert(context.Background(), Record{
		UserID: "user-1", BlockID: "report-1", Judged: false,
		PrevState: corpus.StateTraining, PrevSuccessStreak: 0, CreatedAt: now.Add(-5 * time.Minute),
	}); err != nil {
		t.Fatalf("Insert: %v", err)
	}
	svc.SetRescueSource(stubRescues{counts: map[string]int{"silent": 2, "incomplete": 1}})

	got, err := svc.StuckMap(context.Background(), "user-1", 30)
	if err != nil {
		t.Fatalf("StuckMap: %v", err)
	}
	if got.WindowDays != 30 || got.Note == "" {
		t.Fatalf("response = %+v", got)
	}
	if len(got.Rows) != 2 {
		t.Fatalf("rows = %+v, want one per function tag", got.Rows)
	}
	// Sorted by failures: report (1) then commit (1, the clarify block's tag) —
	// ties break on the tag name.
	byTag := map[string]StuckRow{}
	for _, row := range got.Rows {
		byTag[row.FunctionTag] = row
	}
	report, ok := byTag["report"]
	if !ok {
		t.Fatalf("no report row: %+v", got.Rows)
	}
	if report.Attempts != 2 || report.Failures != 1 || report.ForgotAfterLearning != 1 || report.Promotions != 1 {
		t.Fatalf("report row = %+v", report)
	}
	if report.AvgAttemptsPerPromotion != 2 {
		t.Fatalf("avg attempts per promotion = %v, want 2", report.AvgAttemptsPerPromotion)
	}
	other, ok := byTag["commit"]
	if !ok || other.ForgotAfterLearning != 0 || other.Failures != 1 {
		t.Fatalf("commit row = %+v", other)
	}
	if got.Rescues["silent"] != 2 || got.Rescues["incomplete"] != 1 {
		t.Fatalf("rescues = %+v", got.Rescues)
	}
}

// The window is applied, and a missing rescue source is not an error.
func TestStuckMap_WindowAndOptionalRescues(t *testing.T) {
	svc, blocks, recs, now := configFixture(t)
	seedDue(t, blocks, "user-1", "b1", corpus.StateTraining, now.Add(-time.Hour))
	if _, err := recs.Insert(context.Background(), Record{
		UserID: "user-1", BlockID: "b1", Judged: true, SemanticPass: false,
		PrevState: corpus.StateTraining, CreatedAt: now.AddDate(0, 0, -60),
	}); err != nil {
		t.Fatalf("Insert: %v", err)
	}

	got, err := svc.StuckMap(context.Background(), "user-1", 30)
	if err != nil {
		t.Fatalf("StuckMap: %v", err)
	}
	if len(got.Rows) != 0 {
		t.Fatalf("a failure 60 days old must fall outside a 30-day window: %+v", got.Rows)
	}
	if got.Rescues != nil {
		t.Fatalf("no rescue source means no rescues section: %+v", got.Rescues)
	}

	if _, err := svc.StuckMap(context.Background(), "  ", 30); err == nil {
		t.Fatal("missing user must be rejected")
	}
}
