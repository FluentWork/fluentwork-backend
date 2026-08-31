package corpus

import (
	"testing"
	"time"
)

func TestApplySuccessFromNewToTraining(t *testing.T) {
	now := time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC)
	block := PhraseBlock{
		State:         StateNew,
		SuccessStreak: 0,
		NextDueAt:     now,
	}
	updated := ApplySuccess(block, now)
	if updated.State != StateTraining {
		t.Fatalf("state = %q", updated.State)
	}
	if updated.SuccessStreak != 1 {
		t.Fatalf("success_streak = %d", updated.SuccessStreak)
	}
	want := nextDueDays(now, 1)
	if !updated.NextDueAt.Equal(want) {
		t.Fatalf("next_due_at = %s want %s", updated.NextDueAt, want)
	}
}

func TestApplySuccessTransitionsToAutomated(t *testing.T) {
	now := time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC)
	block := PhraseBlock{
		State:         StateTraining,
		SuccessStreak: 2,
		NextDueAt:     now,
	}
	updated := ApplySuccess(block, now)
	if updated.State != StateAutomated {
		t.Fatalf("state = %q", updated.State)
	}
	if updated.SuccessStreak != 3 {
		t.Fatalf("success_streak = %d", updated.SuccessStreak)
	}
	want := nextDueDays(now, 7)
	if !updated.NextDueAt.Equal(want) {
		t.Fatalf("next_due_at = %s want %s", updated.NextDueAt, want)
	}
}

func TestApplyFailureDropsAutomatedBackToTraining(t *testing.T) {
	now := time.Date(2026, 8, 31, 9, 0, 0, 0, time.UTC)
	block := PhraseBlock{
		State:         StateAutomated,
		SuccessStreak: 5,
		NextDueAt:     now,
	}
	updated := ApplyFailure(block, now)
	if updated.State != StateTraining {
		t.Fatalf("state = %q", updated.State)
	}
	if updated.SuccessStreak != 0 {
		t.Fatalf("success_streak = %d", updated.SuccessStreak)
	}
	want := nextDueDays(now, 1)
	if !updated.NextDueAt.Equal(want) {
		t.Fatalf("next_due_at = %s want %s", updated.NextDueAt, want)
	}
}
