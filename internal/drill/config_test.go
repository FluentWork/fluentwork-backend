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

// 83_ §2.1 风险 2 的"过期任务不累积"：休假回来看到的是正常队列，不是最旧的债。
func TestRound_FoldsAncientOverdueForward(t *testing.T) {
	svc, blocks, _, now := configFixture(t)
	ancient := now.Add(-21 * 24 * time.Hour)
	recent := now.Add(-time.Hour)
	seedDue(t, blocks, "user-1", "ancient", corpus.StateTraining, ancient)
	seedDue(t, blocks, "user-1", "recent", corpus.StateTraining, recent)
	svc.SetConfig(Config{RoundSize: 10, OverdueWindow: 72 * time.Hour})

	if _, err := svc.Round(context.Background(), "user-1", 0); err != nil {
		t.Fatalf("Round: %v", err)
	}

	moved, err := blocks.GetBlock(context.Background(), "user-1", "ancient")
	if err != nil {
		t.Fatalf("GetBlock: %v", err)
	}
	if !moved.NextDueAt.After(now.Add(-time.Minute)) {
		t.Fatalf("ancient block still overdue: %v", moved.NextDueAt)
	}
	untouched, err := blocks.GetBlock(context.Background(), "user-1", "recent")
	if err != nil {
		t.Fatalf("GetBlock: %v", err)
	}
	if !untouched.NextDueAt.Equal(recent) {
		t.Fatalf("a block overdue by an hour must keep its due time: %v", untouched.NextDueAt)
	}
}

// 窗口为 0 即关闭：需要看"真实的过期积压"时（例如运营观察）可以关掉。
func TestRound_OverdueSweepDisabledByZeroWindow(t *testing.T) {
	svc, blocks, _, now := configFixture(t)
	ancient := now.Add(-21 * 24 * time.Hour)
	seedDue(t, blocks, "user-1", "ancient", corpus.StateTraining, ancient)
	svc.SetConfig(Config{RoundSize: 10})

	if _, err := svc.Round(context.Background(), "user-1", 0); err != nil {
		t.Fatalf("Round: %v", err)
	}
	block, err := blocks.GetBlock(context.Background(), "user-1", "ancient")
	if err != nil {
		t.Fatalf("GetBlock: %v", err)
	}
	if !block.NextDueAt.Equal(ancient) {
		t.Fatalf("sweep ran with a zero window: %v", block.NextDueAt)
	}
}

// 账本故障不该让轮次失败：sweep 失败只记 warn。
func TestRound_OverdueSweepFailureStillServes(t *testing.T) {
	svc, blocks, _, now := configFixture(t)
	seedDue(t, blocks, "user-1", "due-1", corpus.StateTraining, now.Add(-time.Minute))
	svc.SetConfig(Config{RoundSize: 10, OverdueWindow: 72 * time.Hour})
	svc.blocks = failingSweepStore{Store: blocks}

	round, err := svc.Round(context.Background(), "user-1", 0)
	if err != nil {
		t.Fatalf("Round: %v", err)
	}
	if round.Size != 1 {
		t.Fatalf("round = %+v, want the due block", round)
	}
}

type failingSweepStore struct {
	corpus.Store
}

func (failingSweepStore) SweepOverdue(context.Context, string, time.Time, time.Time) (int, error) {
	return 0, context.DeadlineExceeded
}

// E4 的"已自动化变化"：客户端要能只对"刚刚变绿"的那一次做庆祝。
func TestJudge_PromotedFlagMarksTheGreenTransition(t *testing.T) {
	svc, blocks, _, now := configFixture(t)
	seedDue(t, blocks, "user-1", "block-1", corpus.StateTraining, now.Add(-time.Minute))
	svc.SetConfig(Config{Schedule: corpus.Schedule{PromoteStreak: 2}})

	first, err := svc.Judge(context.Background(), "user-1", JudgeRequest{BlockID: "block-1", ASRText: "Let's ship it block-1"})
	if err != nil {
		t.Fatalf("first Judge: %v", err)
	}
	if first.Promoted {
		t.Fatalf("first pass is not a promotion: %+v", first)
	}
	second, err := svc.Judge(context.Background(), "user-1", JudgeRequest{BlockID: "block-1", ASRText: "Let's ship it block-1"})
	if err != nil {
		t.Fatalf("second Judge: %v", err)
	}
	if !second.Promoted || second.State != corpus.StateAutomated {
		t.Fatalf("promotion not flagged: %+v", second)
	}
	third, err := svc.Judge(context.Background(), "user-1", JudgeRequest{BlockID: "block-1", ASRText: "Let's ship it block-1"})
	if err != nil {
		t.Fatalf("third Judge: %v", err)
	}
	if third.Promoted {
		t.Fatalf("re-verifying an already green block is not a promotion: %+v", third)
	}
}
