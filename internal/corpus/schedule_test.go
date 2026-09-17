package corpus

import (
	"context"
	"testing"
	"time"
)

// Locks the shared ladder (PRD §5.3.2) at its home in corpus; drill.ApplyJudge
// delegates here, so drill's own tests exercise the same code.
func TestApplyJudge_Ladder(t *testing.T) {
	now := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)

	newBlock := PhraseBlock{State: StateNew, NextDueAt: now}
	p1 := ApplyJudge(newBlock, true, now)
	if p1.State != StateTraining || p1.SuccessStreak != 1 || !p1.NextDueAt.Equal(now.Add(24*time.Hour)) {
		t.Fatalf("first pass = %+v", p1)
	}
	p2 := ApplyJudge(p1, true, now)
	if p2.State != StateTraining || p2.SuccessStreak != 2 || !p2.NextDueAt.Equal(now.Add(24*time.Hour)) {
		t.Fatalf("second pass = %+v", p2)
	}
	p3 := ApplyJudge(p2, true, now)
	if p3.State != StateAutomated || p3.SuccessStreak != 3 || !p3.NextDueAt.Equal(now.Add(7*24*time.Hour)) {
		t.Fatalf("third pass = %+v", p3)
	}
	auto := ApplyJudge(p3, true, now)
	if auto.State != StateAutomated || auto.SuccessStreak != 3 ||
		!auto.NextDueAt.Equal(now.Add(30*24*time.Hour)) {
		t.Fatalf("automated re-pass = %+v", auto)
	}
	fail := ApplyJudge(p3, false, now)
	if fail.State != StateTraining || fail.SuccessStreak != 0 || !fail.NextDueAt.Equal(now.Add(time.Hour)) {
		t.Fatalf("fail from automated = %+v", fail)
	}
	if !p1.UpdatedAt.Equal(now) {
		t.Fatalf("updated_at = %v", p1.UpdatedAt)
	}
}

// E3: the ladder is the server's to configure. A custom schedule must actually
// drive both writers — drill judging and the B7 hit writeback.
func TestSchedule_CustomLadder(t *testing.T) {
	custom := Schedule{
		PromoteStreak:     2,
		TrainingInterval:  5 * time.Minute,
		AutomatedInterval: 2 * time.Hour,
		FailInterval:      time.Minute,
	}
	now := time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC)

	training := custom.ApplyJudge(PhraseBlock{State: StateNew}, true, now)
	if training.State != StateTraining || training.SuccessStreak != 1 {
		t.Fatalf("first pass = %+v", training)
	}
	if !training.NextDueAt.Equal(now.Add(5 * time.Minute)) {
		t.Fatalf("training interval ignored: %v", training.NextDueAt)
	}
	// Two in a row is enough under this schedule, not three.
	promoted := custom.ApplyJudge(training, true, now)
	if promoted.State != StateAutomated || promoted.SuccessStreak != 2 {
		t.Fatalf("promotion = %+v", promoted)
	}
	if !promoted.NextDueAt.Equal(now.Add(2 * time.Hour)) {
		t.Fatalf("automated interval ignored: %v", promoted.NextDueAt)
	}
	failed := custom.ApplyJudge(promoted, false, now)
	if !failed.NextDueAt.Equal(now.Add(time.Minute)) || failed.SuccessStreak != 0 {
		t.Fatalf("fail interval ignored: %+v", failed)
	}
	// An unset review interval still falls back to the PRD default (30d) rather
	// than scheduling a green block for the fail interval.
	review := custom.ApplyJudge(promoted, true, now)
	if !review.NextDueAt.Equal(now.Add(30 * 24 * time.Hour)) {
		t.Fatalf("review interval = %v, want the default", review.NextDueAt)
	}
}

func TestSchedule_NormalizeFillsDefaults(t *testing.T) {
	normalized := Schedule{}.Normalize()
	if normalized != DefaultSchedule() {
		t.Fatalf("zero schedule = %+v, want the PRD defaults", normalized)
	}
	partial := Schedule{PromoteStreak: 5}.Normalize()
	if partial.PromoteStreak != 5 || partial.TrainingInterval != 24*time.Hour {
		t.Fatalf("partial = %+v", partial)
	}
}

// The hit writeback must follow the configured ladder, not the compiled-in one:
// otherwise a real-world hit and a drill answer would promote on different terms.
func TestMemoryStore_RecordHits_UsesConfiguredSchedule(t *testing.T) {
	store := NewMemoryStore()
	store.SetSchedule(Schedule{PromoteStreak: 1, TrainingInterval: 5 * time.Minute})
	seedBlock(t, store, "block-1", "user-1", "推动上线", "Let's ship it.")

	hitAt := time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC)
	if _, err := NewHitsService(store).RecordHits(context.Background(), "user-1", "session-1", "turn-1",
		[]Hit{{BlockID: "block-1", DetectedAtMs: hitAt.UnixMilli()}}); err != nil {
		t.Fatalf("RecordHits: %v", err)
	}
	block, err := store.GetBlock(context.Background(), "user-1", "block-1")
	if err != nil {
		t.Fatalf("GetBlock: %v", err)
	}
	if block.State != StateAutomated {
		t.Fatalf("state = %s, want automated under PromoteStreak=1", block.State)
	}
	if !block.NextDueAt.Equal(hitAt.Add(7 * 24 * time.Hour)) {
		t.Fatalf("next_due_at = %v, want +7d (default automated interval)", block.NextDueAt)
	}
}
