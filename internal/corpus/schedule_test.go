package corpus

import (
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
