package corpus

import (
	"context"
	"database/sql"
	"regexp"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

func TestMySQLStore_RecordHits_InsertThenDuplicate(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := NewMySQLStore(db)

	usedAt := time.UnixMilli(1000).UTC()
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO phrase_block_uses`).
		WithArgs("user-1", "session-1", "turn-1", "block-1", int64(1000)).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(`SELECT state, success_streak, next_due_at`).
		WithArgs("block-1", "user-1").
		WillReturnRows(sqlmock.NewRows([]string{"state", "success_streak", "next_due_at"}).
			AddRow(StateNew, 0, usedAt))
	mock.ExpectExec(`INSERT INTO phrase_block_real_uses`).
		WithArgs("block-1-turn-1", "user-1", "block-1", RealUseSourceHit, "turn-1", usedAt, usedAt).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`UPDATE phrase_blocks`).
		WithArgs(usedAt, StateTraining, 1, usedAt.Add(24*time.Hour), usedAt, "block-1", "user-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	n, err := store.RecordHits(context.Background(), "user-1", "session-1", "turn-1", []Hit{
		{BlockID: "block-1", DetectedAtMs: 1000},
	})
	if err != nil {
		t.Fatalf("insert: %v", err)
	}
	if n != 1 {
		t.Fatalf("recorded = %d", n)
	}

	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO phrase_block_uses`).
		WithArgs("user-1", "session-1", "turn-1", "block-1", int64(1500)).
		WillReturnResult(sqlmock.NewResult(1, 2))
	mock.ExpectCommit()

	n, err = store.RecordHits(context.Background(), "user-1", "session-1", "turn-1", []Hit{
		{BlockID: "block-1", DetectedAtMs: 1500},
	})
	if err != nil {
		t.Fatalf("duplicate: %v", err)
	}
	if n != 1 {
		t.Fatalf("duplicate recorded = %d", n)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

// §5.3.2: an automated block re-hit keeps 绿灯 and pushes +30d.
func TestMySQLStore_RecordHits_AutomatedBlockReschedules30d(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := NewMySQLStore(db)

	usedAt := time.UnixMilli(5_000).UTC()
	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO phrase_block_uses`).
		WithArgs("user-1", "session-1", "turn-1", "block-1", int64(5_000)).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(`SELECT state, success_streak, next_due_at`).
		WithArgs("block-1", "user-1").
		WillReturnRows(sqlmock.NewRows([]string{"state", "success_streak", "next_due_at"}).
			AddRow(StateAutomated, 3, usedAt))
	mock.ExpectExec(`INSERT INTO phrase_block_real_uses`).
		WithArgs("block-1-turn-1", "user-1", "block-1", RealUseSourceHit, "turn-1", usedAt, usedAt).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`UPDATE phrase_blocks`).
		WithArgs(usedAt, StateAutomated, 3, usedAt.Add(30*24*time.Hour), usedAt, "block-1", "user-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	if _, err := store.RecordHits(context.Background(), "user-1", "session-1", "turn-1", []Hit{
		{BlockID: "block-1", DetectedAtMs: 5_000},
	}); err != nil {
		t.Fatalf("RecordHits: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

// A hit for a soft-deleted (or foreign) block writes no counters and no error.
func TestMySQLStore_RecordHits_SkipsMissingBlock(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := NewMySQLStore(db)

	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO phrase_block_uses`).
		WithArgs("user-1", "session-1", "turn-1", "block-gone", int64(7_000)).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectQuery(`SELECT state, success_streak, next_due_at`).
		WithArgs("block-gone", "user-1").
		WillReturnError(sql.ErrNoRows)
	mock.ExpectCommit()

	n, err := store.RecordHits(context.Background(), "user-1", "session-1", "turn-1", []Hit{
		{BlockID: "block-gone", DetectedAtMs: 7_000},
	})
	if err != nil {
		t.Fatalf("RecordHits: %v", err)
	}
	if n != 1 {
		t.Fatalf("recorded = %d, want 1", n)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

func TestMySQLStore_ListSessionHits_JoinsAndOmitsDeleted(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := NewMySQLStore(db)

	rows := sqlmock.NewRows([]string{"block_id", "turn_id", "used_at_ms", "intent_zh", "expression_en"}).
		AddRow("block-1", "turn-1", int64(1000), "推动上线", "Let's ship it.")
	mock.ExpectQuery(regexp.QuoteMeta("FROM phrase_block_uses pbu")).
		WithArgs("session-1").
		WillReturnRows(rows)

	hits, err := store.ListSessionHits(context.Background(), "session-1")
	if err != nil {
		t.Fatalf("ListSessionHits: %v", err)
	}
	if len(hits) != 1 || hits[0].ChunkEN != "Let's ship it." {
		t.Fatalf("hits = %+v", hits)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("sql expectations: %v", err)
	}
}

// RecordRealUses moves the ledger, the counters and the schedule in one
// transaction — a credit that lands without its provenance row cannot be split
// into L1/L2 later.
func TestMySQLStore_RecordRealUses(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := NewMySQLStore(db)
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT state, success_streak, next_due_at FROM phrase_blocks`).
		WithArgs("block-1", "user-1").
		WillReturnRows(sqlmock.NewRows([]string{"state", "success_streak", "next_due_at"}).
			AddRow(StateTraining, 1, now))
	mock.ExpectExec(`INSERT INTO phrase_block_real_uses`).
		WithArgs("card-1-block-1", "user-1", "block-1", RealUseSourceCheckin, "card-1", now, now).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`UPDATE phrase_blocks`).
		WithArgs(now, StateTraining, 2, now.Add(24*time.Hour), now, "block-1", "user-1").
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectCommit()

	credited, err := store.RecordRealUses(context.Background(), "user-1", []string{"block-1"}, RealUseSourceCheckin, "card-1", now)
	if err != nil {
		t.Fatalf("RecordRealUses: %v", err)
	}
	if credited != 1 {
		t.Fatalf("credited = %d", credited)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

// A repeat for the same card credits nothing: the unique key refuses the row and
// the transaction leaves the block alone.
func TestMySQLStore_RecordRealUses_DuplicateIsNoOp(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := NewMySQLStore(db)
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT state, success_streak, next_due_at FROM phrase_blocks`).
		WithArgs("block-1", "user-1").
		WillReturnRows(sqlmock.NewRows([]string{"state", "success_streak", "next_due_at"}).
			AddRow(StateTraining, 2, now))
	mock.ExpectExec(`INSERT INTO phrase_block_real_uses`).
		WithArgs("card-1-block-1", "user-1", "block-1", RealUseSourceCheckin, "card-1", now, now).
		WillReturnResult(sqlmock.NewResult(0, 0))
	mock.ExpectCommit()

	credited, err := store.RecordRealUses(context.Background(), "user-1", []string{"block-1"}, RealUseSourceCheckin, "card-1", now)
	if err != nil {
		t.Fatalf("RecordRealUses: %v", err)
	}
	if credited != 0 {
		t.Fatalf("credited = %d, want 0 on a repeat", credited)
	}
}
