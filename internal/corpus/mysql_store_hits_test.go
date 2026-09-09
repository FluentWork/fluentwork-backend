package corpus

import (
	"context"
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

	mock.ExpectBegin()
	mock.ExpectExec(`INSERT INTO phrase_block_uses`).
		WithArgs("user-1", "session-1", "turn-1", "block-1", int64(1000)).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectExec(`UPDATE phrase_blocks`).
		WithArgs(time.UnixMilli(1000).UTC(), "block-1", "user-1").
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
