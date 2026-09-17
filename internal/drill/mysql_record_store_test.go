package drill

import (
	"context"
	"database/sql"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/FluentWork/fluentwork-backend/internal/corpus"
)

func newRecordStoreMock(t *testing.T) (*MySQLRecordStore, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	return NewMySQLRecordStore(db), mock
}

// The snapshot columns are what make an appeal exact; if they stop being
// written, the appeal silently starts guessing.
func TestMySQLRecordStore_InsertWritesSnapshot(t *testing.T) {
	store, mock := newRecordStoreMock(t)
	due := time.Date(2026, 9, 17, 9, 0, 0, 0, time.UTC)
	created := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)

	mock.ExpectExec(`INSERT INTO drill_records`).
		WithArgs("user-1", "block-1", "session-1", 1, false, true, 1200, "said it wrong",
			"not equivalent", "training", 2, due, created).
		WillReturnResult(sqlmock.NewResult(42, 1))

	id, err := store.Insert(context.Background(), Record{
		UserID:            "user-1",
		BlockID:           "block-1",
		SessionID:         "session-1",
		DrillType:         DrillTypeRecall,
		Judged:            true,
		ResponseMS:        1200,
		ASRText:           "said it wrong",
		JudgeReason:       "not equivalent",
		PrevState:         "training",
		PrevSuccessStreak: 2,
		PrevNextDueAt:     due,
		CreatedAt:         created,
	})
	if err != nil {
		t.Fatalf("Insert: %v", err)
	}
	if id != 42 {
		t.Fatalf("id = %d, want the ledger id", id)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestMySQLRecordStore_GetRecord(t *testing.T) {
	t.Run("missing row is the sentinel", func(t *testing.T) {
		store, mock := newRecordStoreMock(t)
		mock.ExpectQuery(`SELECT .* FROM drill_records`).
			WithArgs(int64(7), "user-1").
			WillReturnError(sql.ErrNoRows)
		if _, err := store.GetRecord(context.Background(), "user-1", 7); err != ErrRecordNotFound {
			t.Fatalf("err = %v, want ErrRecordNotFound", err)
		}
	})

	t.Run("nulls map to zero values", func(t *testing.T) {
		store, mock := newRecordStoreMock(t)
		created := time.Date(2026, 9, 17, 10, 0, 0, 0, time.UTC)
		mock.ExpectQuery(`SELECT .* FROM drill_records`).
			WithArgs(int64(7), "user-1").
			WillReturnRows(sqlmock.NewRows([]string{
				"id", "user_id", "block_id", "session_id", "drill_type", "semantic_pass", "judged",
				"response_ms", "asr_text", "judge_reason", "prev_state", "prev_success_streak",
				"prev_next_due_at", "appealed_at", "created_at",
			}).AddRow(7, "user-1", "block-1", nil, 1, false, true, 900, "text", nil, "", 0, nil, nil, created))

		rec, err := store.GetRecord(context.Background(), "user-1", 7)
		if err != nil {
			t.Fatalf("GetRecord: %v", err)
		}
		if rec.SessionID != "" || rec.JudgeReason != "" || !rec.PrevNextDueAt.IsZero() || rec.AppealedAt != nil {
			t.Fatalf("nulls must scan to zero values: %+v", rec)
		}
	})
}

// A second appeal must not stamp again — the first appeal is the data point.
func TestMySQLRecordStore_MarkAppealed(t *testing.T) {
	t.Run("first appeal stamps", func(t *testing.T) {
		store, mock := newRecordStoreMock(t)
		at := time.Date(2026, 9, 17, 11, 0, 0, 0, time.UTC)
		mock.ExpectExec(`UPDATE drill_records`).
			WithArgs(at, int64(7), "user-1").
			WillReturnResult(sqlmock.NewResult(0, 1))
		first, err := store.MarkAppealed(context.Background(), "user-1", 7, at)
		if err != nil || !first {
			t.Fatalf("first = %v err = %v", first, err)
		}
	})

	t.Run("already stamped reports not-first", func(t *testing.T) {
		store, mock := newRecordStoreMock(t)
		at := time.Date(2026, 9, 17, 11, 0, 0, 0, time.UTC)
		mock.ExpectExec(`UPDATE drill_records`).
			WithArgs(at, int64(7), "user-1").
			WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(`SELECT .* FROM drill_records`).
			WithArgs(int64(7), "user-1").
			WillReturnRows(sqlmock.NewRows([]string{
				"id", "user_id", "block_id", "session_id", "drill_type", "semantic_pass", "judged",
				"response_ms", "asr_text", "judge_reason", "prev_state", "prev_success_streak",
				"prev_next_due_at", "appealed_at", "created_at",
			}).AddRow(7, "user-1", "block-1", nil, 1, false, true, 900, "text", nil, "training", 2, nil, at, at))

		first, err := store.MarkAppealed(context.Background(), "user-1", 7, at)
		if err != nil {
			t.Fatalf("MarkAppealed: %v", err)
		}
		if first {
			t.Fatal("a stamped record must not report a first appeal")
		}
	})

	t.Run("unknown record is the sentinel", func(t *testing.T) {
		store, mock := newRecordStoreMock(t)
		at := time.Date(2026, 9, 17, 11, 0, 0, 0, time.UTC)
		mock.ExpectExec(`UPDATE drill_records`).
			WithArgs(at, int64(7), "user-1").
			WillReturnResult(sqlmock.NewResult(0, 0))
		mock.ExpectQuery(`SELECT .* FROM drill_records`).
			WithArgs(int64(7), "user-1").
			WillReturnError(sql.ErrNoRows)
		if _, err := store.MarkAppealed(context.Background(), "user-1", 7, at); err != ErrRecordNotFound {
			t.Fatalf("err = %v, want ErrRecordNotFound", err)
		}
	})
}

func TestMySQLRecordStore_IsLatestForBlock(t *testing.T) {
	store, mock := newRecordStoreMock(t)
	mock.ExpectQuery(`SELECT MAX\(id\) FROM drill_records`).
		WithArgs("user-1", "block-1").
		WillReturnRows(sqlmock.NewRows([]string{"MAX(id)"}).AddRow(7))
	latest, err := store.IsLatestForBlock(context.Background(), "user-1", "block-1", 7)
	if err != nil || !latest {
		t.Fatalf("latest = %v err = %v", latest, err)
	}

	store, mock = newRecordStoreMock(t)
	mock.ExpectQuery(`SELECT MAX\(id\) FROM drill_records`).
		WithArgs("user-1", "block-1").
		WillReturnRows(sqlmock.NewRows([]string{"MAX(id)"}).AddRow(9))
	latest, err = store.IsLatestForBlock(context.Background(), "user-1", "block-1", 7)
	if err != nil || latest {
		t.Fatalf("latest = %v err = %v, want false", latest, err)
	}

	store, mock = newRecordStoreMock(t)
	mock.ExpectQuery(`SELECT MAX\(id\) FROM drill_records`).
		WithArgs("user-1", "block-1").
		WillReturnRows(sqlmock.NewRows([]string{"MAX(id)"}).AddRow(nil))
	if _, err := store.IsLatestForBlock(context.Background(), "user-1", "block-1", 7); err != ErrRecordNotFound {
		t.Fatalf("err = %v, want ErrRecordNotFound", err)
	}
}

func TestMySQLRecordStore_CountNewReleasesSince(t *testing.T) {
	store, mock := newRecordStoreMock(t)
	day := time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)
	mock.ExpectQuery(`SELECT COUNT\(\*\) FROM drill_records`).
		WithArgs("user-1", "new", day).
		WillReturnRows(sqlmock.NewRows([]string{"COUNT(*)"}).AddRow(3))

	n, err := store.CountNewReleasesSince(context.Background(), "user-1", day)
	if err != nil {
		t.Fatalf("Count: %v", err)
	}
	if n != 3 {
		t.Fatalf("n = %d", n)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

// The round's overdue sweep is one statement; a wrong bound would either sweep
// everything or nothing.
func TestMySQLStore_SweepOverdue(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := corpus.NewMySQLStore(db)
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)
	cutoff := now.Add(-72 * time.Hour)

	mock.ExpectExec(`UPDATE phrase_blocks\s+SET next_due_at = \?, updated_at = \?`).
		WithArgs(now, now, "user-1", cutoff).
		WillReturnResult(sqlmock.NewResult(0, 4))

	moved, err := store.SweepOverdue(context.Background(), "user-1", cutoff, now)
	if err != nil {
		t.Fatalf("SweepOverdue: %v", err)
	}
	if moved != 4 {
		t.Fatalf("moved = %d", moved)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}
