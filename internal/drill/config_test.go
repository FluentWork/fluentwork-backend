package drill

import (
	"context"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/corpus"
)

func configFixture(t *testing.T) (*Service, *corpus.MemoryStore, *MemoryRecordStore, time.Time) {
	t.Helper()
	blocks := corpus.NewMemoryStore()
	recs := NewMemoryRecordStore()
	svc := NewService(blocks, recs, &LLMJudge{LLM: StaticCompleter{Body: `{"pass":true}`}},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	now := time.Date(2026, 9, 18, 9, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }
	return svc, blocks, recs, now
}

func TestRound_UsesConfiguredSize(t *testing.T) {
	svc, blocks, _, now := configFixture(t)
	for _, id := range []string{"b-1", "b-2", "b-3"} {
		seedDue(t, blocks, "user-1", id, corpus.StateTraining, now.Add(-time.Minute))
	}
	svc.SetConfig(Config{RoundSize: 2})

	round, err := svc.Round(context.Background(), "user-1", 0)
	if err != nil {
		t.Fatalf("Round: %v", err)
	}
	if round.Size != 2 {
		t.Fatalf("size = %d, want the configured 2", round.Size)
	}
	// An explicit size still wins over the configured default.
	round, err = svc.Round(context.Background(), "user-1", 3)
	if err != nil {
		t.Fatalf("Round: %v", err)
	}
	if round.Size != 3 {
		t.Fatalf("size = %d, want the requested 3", round.Size)
	}
}

// §5.3.2's 每日新块释放上限: once the day's new blocks are spent, rounds serve
// training material only — the cap defers new blocks, it does not close the drill.
func TestRound_DailyNewBlockLimit(t *testing.T) {
	svc, blocks, _, now := configFixture(t)
	seedDue(t, blocks, "user-1", "new-1", corpus.StateNew, now.Add(-time.Minute))
	seedDue(t, blocks, "user-1", "training-1", corpus.StateTraining, now.Add(-time.Minute))
	svc.SetConfig(Config{DailyNewBlockLimit: 1})

	first, err := svc.Round(context.Background(), "user-1", 0)
	if err != nil {
		t.Fatalf("first Round: %v", err)
	}
	if first.Size != 2 {
		t.Fatalf("first round = %+v, want both due blocks", first)
	}

	// Answering the new block spends the day's budget.
	if _, err := svc.Judge(context.Background(), "user-1", JudgeRequest{
		BlockID: "new-1", ASRText: "Let's ship it new-1",
	}); err != nil {
		t.Fatalf("Judge: %v", err)
	}

	second, err := svc.Round(context.Background(), "user-1", 0)
	if err != nil {
		t.Fatalf("second Round: %v", err)
	}
	for _, card := range second.Cards {
		if card.State == corpus.StateNew {
			t.Fatalf("new block released past the daily cap: %+v", second)
		}
	}
	if second.Size == 0 {
		t.Fatal("the cap must not empty the drill when training material is due")
	}

	// The budget resets at the next UTC day.
	svc.now = func() time.Time { return now.Add(24 * time.Hour) }
	seedDue(t, blocks, "user-1", "new-2", corpus.StateNew, now.Add(24*time.Hour))
	third, err := svc.Round(context.Background(), "user-1", 0)
	if err != nil {
		t.Fatalf("third Round: %v", err)
	}
	found := false
	for _, card := range third.Cards {
		if card.BlockID == "new-2" {
			found = true
		}
	}
	if !found {
		t.Fatalf("a fresh day must release new blocks again: %+v", third)
	}
}

// A ledger failure must not be able to empty a user's drill.
func TestRound_UncappedWhenBudgetUnavailable(t *testing.T) {
	svc, blocks, _, now := configFixture(t)
	seedDue(t, blocks, "user-1", "new-1", corpus.StateNew, now.Add(-time.Minute))
	svc.SetConfig(Config{DailyNewBlockLimit: 1})
	svc.records = failingCountStore{RecordStore: svc.records}

	round, err := svc.Round(context.Background(), "user-1", 0)
	if err != nil {
		t.Fatalf("Round: %v", err)
	}
	if round.Size != 1 {
		t.Fatalf("round = %+v, want the uncapped release", round)
	}
}

// The judge must follow the configured ladder, not the compiled-in one.
func TestJudge_UsesConfiguredSchedule(t *testing.T) {
	svc, blocks, _, now := configFixture(t)
	seedDue(t, blocks, "user-1", "block-1", corpus.StateNew, now.Add(-time.Minute))
	svc.SetConfig(Config{Schedule: corpus.Schedule{
		PromoteStreak:    1,
		TrainingInterval: 5 * time.Minute,
	}})

	result, err := svc.Judge(context.Background(), "user-1", JudgeRequest{BlockID: "block-1", ASRText: "Let's ship it block-1"})
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	if result.State != corpus.StateAutomated || result.SuccessStreak != 1 {
		t.Fatalf("a PromoteStreak=1 ladder must go green on the first pass: %+v", result)
	}
	if want := now.Add(7 * 24 * time.Hour); result.NextDueAt != want.Format(time.RFC3339Nano) {
		t.Fatalf("next_due_at = %s, want %s", result.NextDueAt, want.Format(time.RFC3339Nano))
	}
}

func TestMemoryRecordStore_CountNewReleasesSince(t *testing.T) {
	store := NewMemoryRecordStore()
	day := time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)
	records := []Record{
		{UserID: "user-1", BlockID: "a", PrevState: corpus.StateNew, CreatedAt: day.Add(time.Hour)},
		{UserID: "user-1", BlockID: "b", PrevState: corpus.StateTraining, CreatedAt: day.Add(2 * time.Hour)},
		{UserID: "user-1", BlockID: "c", PrevState: corpus.StateNew, CreatedAt: day.Add(-time.Hour)},
		{UserID: "user-2", BlockID: "d", PrevState: corpus.StateNew, CreatedAt: day.Add(time.Hour)},
	}
	for _, rec := range records {
		if _, err := store.Insert(context.Background(), rec); err != nil {
			t.Fatalf("Insert: %v", err)
		}
	}
	n, err := store.CountNewReleasesSince(context.Background(), "user-1", day)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n != 1 {
		t.Fatalf("n = %d, want only today's new release for user-1", n)
	}
}

// failingCountStore is a RecordStore whose budget read fails, to prove the round
// still runs.
type failingCountStore struct {
	RecordStore
}

func (failingCountStore) CountNewReleasesSince(context.Context, string, time.Time) (int, error) {
	return 0, context.DeadlineExceeded
}
