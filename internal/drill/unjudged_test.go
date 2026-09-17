package drill

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"strings"
	"testing"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/corpus"
)

// failingCompleter stands in for a judge call that never returns in time.
type failingCompleter struct{ err error }

func (f failingCompleter) Complete(context.Context, string) (string, error) { return "", f.err }

// 86_ F1: the judge timing out says nothing about the learner. The old behaviour
// applied the failure ladder — streak zeroed, block demoted — so a correct answer
// was punished because our call was slow.
func TestJudge_UnjudgedAttemptLeavesTheScheduleAlone(t *testing.T) {
	blocks := corpus.NewMemoryStore()
	recs := NewMemoryRecordStore()
	svc := NewService(blocks, recs, &LLMJudge{LLM: failingCompleter{err: context.DeadlineExceeded}},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }
	seedDue(t, blocks, "user-1", "block-1", corpus.StateTraining, now.Add(-time.Minute))
	if _, err := blocks.UpdateSchedule(context.Background(), "user-1", "block-1",
		corpus.StateTraining, 2, now.Add(-time.Minute), now.Add(-time.Hour)); err != nil {
		t.Fatalf("seed streak: %v", err)
	}
	before, err := blocks.GetBlock(context.Background(), "user-1", "block-1")
	if err != nil {
		t.Fatalf("GetBlock: %v", err)
	}

	resp, err := svc.Judge(context.Background(), "user-1", JudgeRequest{
		BlockID: "block-1", ASRText: "The deploy is on hold.",
	})
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	if resp.Judged {
		t.Fatalf("a timed-out judge must not report a verdict: %+v", resp)
	}
	if !resp.Retryable {
		t.Fatalf("an unjudged attempt must be retryable: %+v", resp)
	}
	if resp.JudgeReason != "judge_timeout" {
		t.Fatalf("reason = %q", resp.JudgeReason)
	}

	after, err := blocks.GetBlock(context.Background(), "user-1", "block-1")
	if err != nil {
		t.Fatalf("GetBlock: %v", err)
	}
	if after.SuccessStreak != before.SuccessStreak || after.State != before.State ||
		!after.NextDueAt.Equal(before.NextDueAt) {
		t.Fatalf("schedule moved on an unjudged attempt: before=%+v after=%+v", before, after)
	}
	if resp.SuccessStreak != before.SuccessStreak || resp.State != before.State {
		t.Fatalf("response should report the untouched schedule: %+v", resp)
	}

	// The attempt is still on the record, marked as unjudged.
	records := recs.Records()
	if len(records) != 1 {
		t.Fatalf("records = %+v", records)
	}
	if records[0].Judged || records[0].SemanticPass {
		t.Fatalf("record must be stored unjudged: %+v", records[0])
	}
	if records[0].JudgeReason != "judge_timeout" {
		t.Fatalf("reason = %q", records[0].JudgeReason)
	}
}

// The attempt spent a daily new-block release, so it must still count as one —
// otherwise a flaky judge would let users exceed the cap.
func TestJudge_UnjudgedAttemptStillSpendsTheRelease(t *testing.T) {
	blocks := corpus.NewMemoryStore()
	recs := NewMemoryRecordStore()
	svc := NewService(blocks, recs, &LLMJudge{LLM: failingCompleter{err: errors.New("boom")}},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }
	seedDue(t, blocks, "user-1", "new-1", corpus.StateNew, now.Add(-time.Minute))

	if _, err := svc.Judge(context.Background(), "user-1", JudgeRequest{BlockID: "new-1", ASRText: "x"}); err != nil {
		t.Fatalf("Judge: %v", err)
	}
	released, err := recs.CountNewReleasesSince(context.Background(), "user-1", now.Add(-time.Hour))
	if err != nil {
		t.Fatalf("CountNewReleasesSince: %v", err)
	}
	if released != 1 {
		t.Fatalf("released = %d, want the unjudged attempt counted", released)
	}
}

// An attempt with no verdict has nothing to appeal: the appeal says so instead
// of pretending to restore something.
func TestAppeal_UnjudgedAttemptHasNothingToRestore(t *testing.T) {
	blocks := corpus.NewMemoryStore()
	recs := NewMemoryRecordStore()
	svc := NewService(blocks, recs, &LLMJudge{LLM: failingCompleter{err: errors.New("boom")}},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }
	seedDue(t, blocks, "user-1", "block-1", corpus.StateTraining, now.Add(-time.Minute))

	judged, err := svc.Judge(context.Background(), "user-1", JudgeRequest{BlockID: "block-1", ASRText: "x"})
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	appeal, err := svc.Appeal(context.Background(), "user-1", AppealRequest{RecordID: judged.RecordID})
	if err != nil {
		t.Fatalf("Appeal: %v", err)
	}
	if appeal.Restored {
		t.Fatalf("nothing was charged, so nothing to restore: %+v", appeal)
	}
	if !strings.Contains(appeal.Note, "not judged") {
		t.Fatalf("note = %q, want it to say the attempt was not judged", appeal.Note)
	}
}

// The rubric is the executable form of PRD §7.5's "语义等价而非逐字一致": the
// omissions the old prompt failed on are now explicitly allowed.
func TestJudgePrompt_StatesTheGistRule(t *testing.T) {
	prompt := JudgePrompt("The deploy is on hold because the migration has not finished.", "The deploy is blocked on the migration.")
	// Assertions stay inside one line of the template: the prompt is wrapped for
	// readability, so a phrase spanning a line break would be a brittle match.
	for _, want := range []string{
		"same core proposition",
		"shorter",
		"leaves out optional detail",
		"omitted_details",
		"Do not require the learner to reproduce every detail",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q:\n%s", want, prompt)
		}
	}
}
