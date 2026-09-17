package drill

import (
	"context"
	"errors"
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/apierr"
	"github.com/FluentWork/fluentwork-backend/internal/corpus"
)

// appealFixture returns a service whose clock is pinned, so next_due_at
// comparisons are exact rather than approximate.
func appealFixture(t *testing.T, judgeBody string) (*Service, *corpus.MemoryStore, *MemoryRecordStore, time.Time) {
	t.Helper()
	blocks := corpus.NewMemoryStore()
	recs := NewMemoryRecordStore()
	svc := NewService(blocks, recs, &LLMJudge{LLM: StaticCompleter{Body: judgeBody}},
		slog.New(slog.NewTextHandler(io.Discard, nil)))
	now := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }
	return svc, blocks, recs, now
}

func seedTrainedBlock(t *testing.T, blocks *corpus.MemoryStore, userID, id string, streak int, due time.Time) {
	t.Helper()
	created := due.Add(-48 * time.Hour)
	if _, err := blocks.SaveAcceptedBlocks(context.Background(), []corpus.PhraseBlock{{
		ID:             id,
		UserID:         userID,
		IntentZH:       "推动上线",
		ExpressionEN:   "Let's ship it",
		AnchorUserSaid: "ship it",
		SceneTag:       "review",
		FunctionTag:    "commit",
		State:          corpus.StateTraining,
		SuccessStreak:  streak,
		NextDueAt:      due,
		EaseFactor:     2.5,
		CreatedAt:      created,
		UpdatedAt:      created,
	}}); err != nil {
		t.Fatalf("seed %s: %v", id, err)
	}
}

// A failed attempt costs the user a streak they had earned. The appeal puts the
// schedule back exactly — streak, state and the original due time — while the
// round's settlement keeps the judge's verdict (PRD §7.5).
func TestAppeal_RestoresScheduleAndKeepsSettlement(t *testing.T) {
	svc, blocks, recs, now := appealFixture(t, `{"pass":false,"judge_reason":"not equivalent"}`)
	due := now.Add(-time.Minute)
	seedTrainedBlock(t, blocks, "user-1", "block-1", 2, due)

	judged, err := svc.Judge(context.Background(), "user-1", JudgeRequest{
		BlockID: "block-1", ASRText: "we should ship it maybe",
	})
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	if judged.Pass {
		t.Fatal("fixture expects a failed attempt")
	}
	if judged.RecordID == 0 {
		t.Fatal("judge response must name the attempt so it can be appealed")
	}
	if judged.ASRText != "we should ship it maybe" {
		t.Fatalf("asr_text = %q, want the text the judge read", judged.ASRText)
	}
	afterFail, err := blocks.GetBlock(context.Background(), "user-1", "block-1")
	if err != nil {
		t.Fatalf("GetBlock: %v", err)
	}
	if afterFail.SuccessStreak != 0 {
		t.Fatalf("streak after fail = %d, want 0", afterFail.SuccessStreak)
	}

	appeal, err := svc.Appeal(context.Background(), "user-1", AppealRequest{RecordID: judged.RecordID})
	if err != nil {
		t.Fatalf("Appeal: %v", err)
	}
	if !appeal.Restored || appeal.AlreadyAppealed {
		t.Fatalf("appeal = %+v", appeal)
	}

	restored, err := blocks.GetBlock(context.Background(), "user-1", "block-1")
	if err != nil {
		t.Fatalf("GetBlock: %v", err)
	}
	if restored.SuccessStreak != 2 || restored.State != corpus.StateTraining {
		t.Fatalf("schedule not restored: streak=%d state=%s", restored.SuccessStreak, restored.State)
	}
	if !restored.NextDueAt.Equal(due) {
		t.Fatalf("next_due_at = %v, want the original %v (未做判定)", restored.NextDueAt, due)
	}
	if appeal.SuccessStreak != 2 || appeal.State != corpus.StateTraining {
		t.Fatalf("appeal response = %+v", appeal)
	}

	// 本轮结算照实算：the ledger keeps the verdict it recorded.
	records := recs.Records()
	if len(records) != 1 || records[0].SemanticPass {
		t.Fatalf("settlement changed: %+v", records)
	}
	if records[0].AppealedAt == nil {
		t.Fatal("appeal must be recorded for the data reflux (§14.3)")
	}
	if records[0].PrevState != corpus.StateTraining || records[0].PrevSuccessStreak != 2 {
		t.Fatalf("snapshot = %+v", records[0])
	}
}

func TestAppeal_SecondAppealChangesNothing(t *testing.T) {
	svc, blocks, _, now := appealFixture(t, `{"pass":false}`)
	seedTrainedBlock(t, blocks, "user-1", "block-1", 2, now.Add(-time.Minute))
	judged, err := svc.Judge(context.Background(), "user-1", JudgeRequest{BlockID: "block-1", ASRText: "nope"})
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	if _, err := svc.Appeal(context.Background(), "user-1", AppealRequest{RecordID: judged.RecordID}); err != nil {
		t.Fatalf("first appeal: %v", err)
	}
	second, err := svc.Appeal(context.Background(), "user-1", AppealRequest{RecordID: judged.RecordID})
	if err != nil {
		t.Fatalf("second appeal: %v", err)
	}
	if !second.AlreadyAppealed || second.Restored {
		t.Fatalf("second appeal = %+v", second)
	}
	if second.SuccessStreak != 2 || second.State != corpus.StateTraining {
		t.Fatalf("response must still report the block state: %+v", second)
	}
}

// An appeal on an old attempt must not roll the block back over a newer one.
func TestAppeal_DoesNotRollBackNewerAttempt(t *testing.T) {
	svc, blocks, _, now := appealFixture(t, `{"pass":false}`)
	seedTrainedBlock(t, blocks, "user-1", "block-1", 2, now.Add(-time.Minute))
	failed, err := svc.Judge(context.Background(), "user-1", JudgeRequest{BlockID: "block-1", ASRText: "nope"})
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	// A second attempt lands before the user gets around to appealing the first.
	svc.judge = &LLMJudge{LLM: StaticCompleter{Body: `{"pass":true}`}}
	if _, err := svc.Judge(context.Background(), "user-1", JudgeRequest{BlockID: "block-1", ASRText: "Let's ship it"}); err != nil {
		t.Fatalf("second Judge: %v", err)
	}
	before, err := blocks.GetBlock(context.Background(), "user-1", "block-1")
	if err != nil {
		t.Fatalf("GetBlock: %v", err)
	}

	appeal, err := svc.Appeal(context.Background(), "user-1", AppealRequest{RecordID: failed.RecordID})
	if err != nil {
		t.Fatalf("Appeal: %v", err)
	}
	if appeal.Restored {
		t.Fatalf("must not roll back over a newer attempt: %+v", appeal)
	}
	if appeal.Note == "" {
		t.Fatal("a refusal must say why")
	}
	after, err := blocks.GetBlock(context.Background(), "user-1", "block-1")
	if err != nil {
		t.Fatalf("GetBlock: %v", err)
	}
	if after.SuccessStreak != before.SuccessStreak || !after.NextDueAt.Equal(before.NextDueAt) {
		t.Fatalf("block moved: before=%+v after=%+v", before, after)
	}
}

// A pass already gave the user credit; there is nothing to undo.
func TestAppeal_PassedAttemptNotRestored(t *testing.T) {
	svc, blocks, _, now := appealFixture(t, `{"pass":true}`)
	seedTrainedBlock(t, blocks, "user-1", "block-1", 2, now.Add(-time.Minute))
	judged, err := svc.Judge(context.Background(), "user-1", JudgeRequest{BlockID: "block-1", ASRText: "Let's ship it"})
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	appeal, err := svc.Appeal(context.Background(), "user-1", AppealRequest{RecordID: judged.RecordID})
	if err != nil {
		t.Fatalf("Appeal: %v", err)
	}
	if appeal.Restored || appeal.Note == "" {
		t.Fatalf("appeal = %+v", appeal)
	}
	block, err := blocks.GetBlock(context.Background(), "user-1", "block-1")
	if err != nil {
		t.Fatalf("GetBlock: %v", err)
	}
	if block.SuccessStreak != 3 {
		t.Fatalf("streak = %d, want the earned 3", block.SuccessStreak)
	}
}

// Another user's attempt is not found — not forbidden: saying it exists is the
// leak (same rule as the judge's ownership check).
func TestAppeal_RejectsForeignRecord(t *testing.T) {
	svc, blocks, _, now := appealFixture(t, `{"pass":false}`)
	seedTrainedBlock(t, blocks, "user-2", "block-2", 1, now.Add(-time.Minute))
	judged, err := svc.Judge(context.Background(), "user-2", JudgeRequest{BlockID: "block-2", ASRText: "nope"})
	if err != nil {
		t.Fatalf("Judge: %v", err)
	}
	_, err = svc.Appeal(context.Background(), "user-1", AppealRequest{RecordID: judged.RecordID})
	var apiErr *apierr.Error
	if !errors.As(err, &apiErr) || apiErr.HTTPStatus != 404 {
		t.Fatalf("err = %v, want 404", err)
	}
}

func TestAppeal_ValidatesRecordID(t *testing.T) {
	svc, _, _, _ := appealFixture(t, `{"pass":false}`)
	if _, err := svc.Appeal(context.Background(), "user-1", AppealRequest{}); err == nil {
		t.Fatal("record_id is required")
	}
	if _, err := svc.Appeal(context.Background(), "user-1", AppealRequest{RecordID: 999}); err == nil {
		t.Fatal("unknown record must 404")
	}
	if _, err := svc.Appeal(context.Background(), "  ", AppealRequest{RecordID: 1}); err == nil {
		t.Fatal("missing user must be rejected")
	}
}
