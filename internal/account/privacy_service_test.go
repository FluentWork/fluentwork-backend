package account_test

import (
	"context"
	"errors"
	"testing"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/account"
	"github.com/FluentWork/fluentwork-backend/internal/aicost"
	"github.com/FluentWork/fluentwork-backend/internal/apierr"
	"github.com/FluentWork/fluentwork-backend/internal/corpus"
	"github.com/FluentWork/fluentwork-backend/internal/drill"
	"github.com/FluentWork/fluentwork-backend/internal/session"
)

type failWiper struct {
	entity string
	err    error
}

func (w *failWiper) EntityType() string { return w.entity }
func (w *failWiper) Wipe(context.Context, string, time.Time) (int, error) {
	return 0, w.err
}
func (w *failWiper) Restore(context.Context, string) (int, error) { return 0, nil }

func newPrivacyFixture(t *testing.T) (*account.PrivacyService, *account.MemoryStore, *corpus.MemoryStore, *session.MemoryStore, *aicost.MemoryStore, *drill.MemoryRecordStore, account.User) {
	t.Helper()
	users := account.NewMemoryStore()
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	user := account.User{ID: "user-1", Status: account.UserStatusActive, CreatedAt: now, UpdatedAt: now}
	if err := users.CreateUser(context.Background(), user); err != nil {
		t.Fatalf("create user: %v", err)
	}
	blocks := corpus.NewMemoryStore()
	if _, err := blocks.SaveAcceptedBlocks(context.Background(), []corpus.PhraseBlock{{
		ID:             "block-1",
		UserID:         user.ID,
		IntentZH:       "推动上线",
		ExpressionEN:   "Let's ship it",
		AnchorUserSaid: "ship it",
		SceneTag:       "review",
		FunctionTag:    "commit",
		State:          corpus.StateNew,
		NextDueAt:      now,
		EaseFactor:     2.5,
		CreatedAt:      now,
		UpdatedAt:      now,
	}}); err != nil {
		t.Fatalf("seed block: %v", err)
	}
	sessions := session.NewMemoryStore()
	if err := sessions.CreateSession(context.Background(), session.Session{
		ID:        "sess-1",
		UserID:    user.ID,
		SceneType: session.DefaultSceneType,
		Status:    session.StatusCreated,
		CreatedAt: now,
		UpdatedAt: now,
	}); err != nil {
		t.Fatalf("seed session: %v", err)
	}
	costs := aicost.NewMemoryStore()
	uid := user.ID
	if err := costs.CreateLog(context.Background(), aicost.Log{
		ID:        "cost-1",
		UserID:    &uid,
		TaskType:  "review",
		Model:     "ark",
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("seed cost: %v", err)
	}
	recs := drill.NewMemoryRecordStore()
	if err := recs.Insert(context.Background(), drill.Record{UserID: user.ID, BlockID: "block-1", ASRText: "hi", CreatedAt: now}); err != nil {
		t.Fatalf("seed record: %v", err)
	}
	svc := account.NewPrivacyService(users, []account.DataWiper{
		corpus.PrivacyWiper{Store: blocks},
		session.PrivacyWiper{Store: sessions},
		aicost.PrivacyWiper{Store: costs},
	}, []account.HardDeleter{drill.RecordWiper{Store: recs}}, nil)
	svc.SetClock(func() time.Time { return now })
	return svc, users, blocks, sessions, costs, recs, user
}

func TestPrivacyService_DeleteIdempotentAndMatrix(t *testing.T) {
	svc, users, blocks, sessions, costs, recs, user := newPrivacyFixture(t)
	ctx := context.Background()
	first, err := svc.DeleteAllData(ctx, user.ID, account.ConfirmationDeleteMyData)
	if err != nil {
		t.Fatalf("delete: %v", err)
	}
	if first.AlreadyDeleted || first.Cascaded["phrase_blocks"] != 1 || first.Cascaded["practice_sessions"] != 1 {
		t.Fatalf("first = %+v", first)
	}
	if first.Cascaded["drill_records"] != 1 || first.Cascaded["ai_cost_logs"] != 1 {
		t.Fatalf("cascade = %+v", first.Cascaded)
	}
	got, err := blocks.GetBlock(ctx, user.ID, "block-1")
	if err == nil && got.DeletedAt == nil {
		t.Fatal("expected phrase block soft-deleted")
	}
	sess, err := sessions.GetSession(ctx, "sess-1")
	if err != nil || sess.DeletedAt == nil {
		t.Fatalf("session soft-delete: %+v err=%v", sess, err)
	}
	if n := len(recs.Records()); n != 0 {
		t.Fatalf("drill records remaining = %d", n)
	}
	recent, err := costs.ListRecent(ctx, user.ID, 10)
	if err != nil || len(recent) != 0 {
		t.Fatalf("anonymized list = %v err=%v", recent, err)
	}
	u, err := users.GetUser(ctx, user.ID)
	if err != nil || u.DeletedAt == nil || u.Status != account.UserStatusDeleted {
		t.Fatalf("user = %+v err=%v", u, err)
	}
	if len(users.Tombstones()) == 0 {
		t.Fatal("expected tombstones")
	}

	second, err := svc.DeleteAllData(ctx, user.ID, account.ConfirmationDeleteMyData)
	if err != nil {
		t.Fatalf("second delete: %v", err)
	}
	if !second.AlreadyDeleted {
		t.Fatalf("second = %+v", second)
	}
}

func TestPrivacyService_WrongConfirmation(t *testing.T) {
	svc, _, _, _, _, _, user := newPrivacyFixture(t)
	_, err := svc.DeleteAllData(context.Background(), user.ID, "nope")
	var apiErr *apierr.Error
	if !errors.As(err, &apiErr) || apiErr.HTTPStatus != 422 {
		t.Fatalf("err = %v", err)
	}
}

func TestPrivacyService_WiperFailureRestores(t *testing.T) {
	_, users, blocks, sessions, costs, recs, user := newPrivacyFixture(t)
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	svc := account.NewPrivacyService(users, []account.DataWiper{
		corpus.PrivacyWiper{Store: blocks},
		session.PrivacyWiper{Store: sessions},
		aicost.PrivacyWiper{Store: costs},
		&failWiper{entity: "boom", err: errors.New("wipe failed")},
	}, []account.HardDeleter{drill.RecordWiper{Store: recs}}, nil)
	svc.SetClock(func() time.Time { return now })
	if _, err := svc.DeleteAllData(context.Background(), user.ID, account.ConfirmationDeleteMyData); err == nil {
		t.Fatal("expected wipe failure")
	}
	u, err := users.GetUser(context.Background(), user.ID)
	if err != nil || u.DeletedAt != nil {
		t.Fatalf("user should remain active: %+v err=%v", u, err)
	}
	got, err := blocks.GetBlock(context.Background(), user.ID, "block-1")
	if err != nil || got.DeletedAt != nil {
		t.Fatalf("block should remain: %+v err=%v", got, err)
	}
}

func TestPrivacyService_UndeleteWindow(t *testing.T) {
	svc, users, blocks, _, _, _, user := newPrivacyFixture(t)
	ctx := context.Background()
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	if _, err := svc.DeleteAllData(ctx, user.ID, account.ConfirmationDeleteMyData); err != nil {
		t.Fatalf("delete: %v", err)
	}

	svc.SetClock(func() time.Time { return now.Add(31 * 24 * time.Hour) })
	_, err := svc.UndeleteUser(ctx, user.ID, "ops", "too late")
	var apiErr *apierr.Error
	if !errors.As(err, &apiErr) || apiErr.HTTPStatus != 422 {
		t.Fatalf("expired err = %v", err)
	}

	svc.SetClock(func() time.Time { return now.Add(2 * 24 * time.Hour) })
	got, err := svc.UndeleteUser(ctx, user.ID, "ops", "user asked")
	if err != nil {
		t.Fatalf("undelete: %v", err)
	}
	if got.Restored["phrase_blocks"] != 1 || got.Restored["users"] != 1 {
		t.Fatalf("restored = %+v", got.Restored)
	}
	u, err := users.GetUser(ctx, user.ID)
	if err != nil || u.DeletedAt != nil || u.Status != account.UserStatusActive {
		t.Fatalf("user after undelete = %+v err=%v", u, err)
	}
	block, err := blocks.GetBlock(ctx, user.ID, "block-1")
	if err != nil || block.DeletedAt != nil {
		t.Fatalf("block after undelete = %+v err=%v", block, err)
	}
	if n := len(users.Tombstones()); n != 0 {
		t.Fatalf("tombstones remaining = %d", n)
	}
	if n := len(users.Audits()); n != 1 {
		t.Fatalf("audits = %d", n)
	}

	_, err = svc.UndeleteUser(ctx, user.ID, "ops", "again")
	if !errors.As(err, &apiErr) || apiErr.HTTPStatus != 422 {
		t.Fatalf("not-deleted err = %v", err)
	}
}

func TestPrivacyService_ExportEnqueue(t *testing.T) {
	svc, _, _, _, _, _, user := newPrivacyFixture(t)
	got, err := svc.EnqueueExport(context.Background(), user.ID, "a@example.com")
	if err != nil {
		t.Fatalf("export: %v", err)
	}
	if got.ExportID == "" || got.EmailTo != "a@example.com" {
		t.Fatalf("got = %+v", got)
	}
	if _, ok := svc.ExportJob(got.ExportID); !ok {
		t.Fatal("job missing")
	}
	again, err := svc.EnqueueExport(context.Background(), user.ID, "b@example.com")
	if err != nil {
		t.Fatalf("second export: %v", err)
	}
	if again.ExportID == got.ExportID {
		t.Fatal("export_id should be unique")
	}
}
