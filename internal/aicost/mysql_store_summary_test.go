package aicost

import (
	"context"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"
)

// chars 是新列：插入与读取两处都要带上，否则 TTS 的计费量会静默丢失。
func TestMySQLStore_CharsColumn(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := NewMySQLStore(db)
	now := time.Date(2026, 9, 18, 12, 0, 0, 0, time.UTC)

	mock.ExpectExec(`INSERT INTO ai_cost_logs`).
		WithArgs("log-1", nil, TaskTypeVoiceTTS, "voice-a", 0, 0, 0, 321, 0, now).
		WillReturnResult(sqlmock.NewResult(1, 1))
	if err := store.CreateLog(context.Background(), Log{
		ID: "log-1", TaskType: TaskTypeVoiceTTS, Model: "voice-a", Chars: 321, CreatedAt: now,
	}); err != nil {
		t.Fatalf("CreateLog: %v", err)
	}

	mock.ExpectQuery(`SELECT id, user_id, task_type, model, tokens_in, tokens_out, audio_sec, chars, cost_micro_yuan, created_at`).
		WithArgs(10).
		WillReturnRows(sqlmock.NewRows([]string{
			"id", "user_id", "task_type", "model", "tokens_in", "tokens_out", "audio_sec", "chars", "cost_fen", "created_at",
		}).AddRow("log-1", nil, TaskTypeVoiceTTS, "voice-a", 0, 0, 0, 321, 0, now))

	logs, err := store.ListRecent(context.Background(), "", 10)
	if err != nil {
		t.Fatalf("ListRecent: %v", err)
	}
	if len(logs) != 1 || logs[0].Chars != 321 {
		t.Fatalf("logs = %+v", logs)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestMySQLStore_SummarizeCosts(t *testing.T) {
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	t.Cleanup(func() { _ = db.Close() })
	store := NewMySQLStore(db)
	since := time.Date(2026, 9, 11, 0, 0, 0, 0, time.UTC)
	until := time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)

	t.Run("by task type", func(t *testing.T) {
		mock.ExpectQuery(`SELECT task_type AS bucket, COUNT\(\*\)`).
			WithArgs(since, until).
			WillReturnRows(sqlmock.NewRows([]string{"bucket", "c", "tin", "tout", "audio", "chars", "fen"}).
				AddRow(TaskTypeVoiceDuplex, 4, 0, 0, 120, 0, 0).
				AddRow(TaskTypeVoiceTTS, 1, 0, 0, 0, 300, 0))
		rows, err := store.SummarizeCosts(context.Background(), SummaryFilter{Since: since, Until: until, GroupBy: GroupByTaskType})
		if err != nil {
			t.Fatalf("SummarizeCosts: %v", err)
		}
		if len(rows) != 2 || rows[0].Key != TaskTypeVoiceDuplex || rows[1].Chars != 300 {
			t.Fatalf("rows = %+v", rows)
		}
	})

	t.Run("by day uses DATE()", func(t *testing.T) {
		mock.ExpectQuery(`SELECT DATE\(created_at\) AS bucket`).
			WithArgs(since, until, "user-1").
			WillReturnRows(sqlmock.NewRows([]string{"bucket", "c", "tin", "tout", "audio", "chars", "fen"}).
				AddRow(time.Date(2026, 9, 17, 0, 0, 0, 0, time.UTC), 2, 10, 20, 30, 0, 0))
		rows, err := store.SummarizeCosts(context.Background(), SummaryFilter{
			UserID: "user-1", Since: since, Until: until, GroupBy: GroupByDay,
		})
		if err != nil {
			t.Fatalf("SummarizeCosts: %v", err)
		}
		if len(rows) != 1 || rows[0].Key != "2026-09-17" || rows[0].TokensIn != 10 {
			t.Fatalf("rows = %+v", rows)
		}
	})
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}
