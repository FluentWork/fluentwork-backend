package corpus

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// MySQL reports 1 for a fresh insert and 0 for an ON DUPLICATE KEY update that
// changes nothing — the difference between "the user told us something new" and
// "the same button was tapped twice".
func TestMySQLStore_SaveFeedback_ReportsFreshInsertOnly(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := NewMySQLStore(db)
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)

	mock.ExpectExec(`INSERT INTO phrase_block_feedback`).
		WithArgs("f1", "user-1", "block-1", FeedbackNotIdiomatic, now).
		WillReturnResult(sqlmock.NewResult(1, 1))
	first, err := store.SaveFeedback(context.Background(), Feedback{
		ID: "f1", UserID: "user-1", BlockID: "block-1", Reason: FeedbackNotIdiomatic, CreatedAt: now,
	})
	if err != nil || !first {
		t.Fatalf("first = %v err = %v", first, err)
	}

	mock.ExpectExec(`INSERT INTO phrase_block_feedback`).
		WithArgs("f2", "user-1", "block-1", FeedbackNotIdiomatic, now).
		WillReturnResult(sqlmock.NewResult(0, 0))
	first, err = store.SaveFeedback(context.Background(), Feedback{
		ID: "f2", UserID: "user-1", BlockID: "block-1", Reason: FeedbackNotIdiomatic, CreatedAt: now,
	})
	if err != nil {
		t.Fatalf("second: %v", err)
	}
	if first {
		t.Fatal("a duplicate tap must not count as a new signal")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestMySQLStore_CountFeedback(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := NewMySQLStore(db)

	mock.ExpectQuery(`SELECT reason, COUNT\(\*\) FROM phrase_block_feedback`).
		WithArgs("user-1").
		WillReturnRows(sqlmock.NewRows([]string{"reason", "COUNT(*)"}).
			AddRow(FeedbackNotIdiomatic, 3).
			AddRow(FeedbackWrongMeaning, 1))
	counts, err := store.CountFeedback(context.Background(), "user-1")
	if err != nil {
		t.Fatalf("CountFeedback: %v", err)
	}
	if counts[FeedbackNotIdiomatic] != 3 || counts[FeedbackWrongMeaning] != 1 {
		t.Fatalf("counts = %+v", counts)
	}
}

func TestMySQLStore_FeedbackWipeStatements(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := NewMySQLStore(db)
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)

	mock.ExpectExec(`UPDATE phrase_block_feedback\s+SET deleted_at = \?`).
		WithArgs(now, "user-1").
		WillReturnResult(sqlmock.NewResult(0, 2))
	n, err := store.SoftDeleteFeedbackForUser(context.Background(), "user-1", now)
	if err != nil || n != 2 {
		t.Fatalf("wipe = %d err = %v", n, err)
	}

	mock.ExpectExec(`UPDATE phrase_block_feedback\s+SET deleted_at = NULL`).
		WithArgs("user-1").
		WillReturnResult(sqlmock.NewResult(0, 2))
	n, err = store.RestoreFeedbackForUser(context.Background(), "user-1")
	if err != nil || n != 2 {
		t.Fatalf("restore = %d err = %v", n, err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}
