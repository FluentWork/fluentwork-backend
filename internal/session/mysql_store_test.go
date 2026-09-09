package session

import (
	"context"
	"database/sql"
	"errors"
	"regexp"
	"strings"
	"testing"
	"time"

	"github.com/DATA-DOG/go-sqlmock"

	"github.com/FluentWork/fluentwork-backend/internal/aicost"
)

// #21 (B8 followup) — MySQL integration tests for MarkSessionReviewedWithCost
// using DATA-DOG/go-sqlmock. These verify the SQL strings, transaction
// boundaries, and idempotency semantics without requiring a running MySQL
// server. The same coverage exists on MemoryStore (see service_test.go) for
// the in-process path; this file covers the production SQL path.

// sessionColumnCount must match scanSession's row.Scan arity in mysql_store.go.
const sessionColumnCount = 10

func newMySQLStoreMock(t *testing.T) (*MySQLStore, sqlmock.Sqlmock, func()) {
	t.Helper()
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	// MarkSessionReviewedWithCost delegates the cost INSERT to
	// aicost.MySQLStore.RecordCostTx — wire it so the production code path
	// (costTx → aicost.RecordCostTx → INSERT INTO ai_cost_logs) is what the
	// tests exercise, not a duplicated inline INSERT. Both stores share the
	// same sqlmock handle so expectations are matched against the same Tx.
	costStore := aicost.NewMySQLStore(mockDB)
	store := NewMySQLStore(mockDB)
	store.SetCostTx(costStore.RecordCostTx)
	cleanup := func() { _ = mockDB.Close() }
	return store, mock, cleanup
}

// sessionColumns is exported for matching (the production constant lives in
// mysql_store.go as an unexported string).
var sessionColumnsRE = regexp.QuoteMeta("id, user_id, material_id, scene_type, status, duration_sec, review_json, created_at, updated_at, deleted_at")

// aicostUserIDArg mirrors aicost.nullableString for the user_id column: nil
// pointer → nil driver arg, otherwise the trimmed string. The production path
// goes through aicost.MySQLStore.RecordCostTx which calls nullableString, so
// the test must expect the post-conversion value — sqlmock matches arguments
// by reflect.TypeOf which would reject *string vs string.
func aicostUserIDArg(value *string) any {
	if value == nil || strings.TrimSpace(*value) == "" {
		return nil
	}
	return strings.TrimSpace(*value)
}

// endedSessionRows returns a Row set for a session with status="ended".
// MaterialID is NULL, ReviewJSON is empty — the test supplies its own via
// UPDATE, not via the SELECT.
func endedSessionRows(id, userID string, at time.Time) *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "user_id", "material_id", "scene_type",
		"status", "duration_sec", "review_json", "created_at", "updated_at", "deleted_at",
	}).AddRow(
		id, userID, nil, "standup",
		StatusEnded, 30, []byte{}, at, at, nil,
	)
}

func reviewedSessionRows(id, userID string, at time.Time) *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "user_id", "material_id", "scene_type",
		"status", "duration_sec", "review_json", "created_at", "updated_at", "deleted_at",
	}).AddRow(
		id, userID, nil, "standup",
		StatusReviewed, 30, []byte(`{"status":"ready"}`), at, at, nil,
	)
}

func conflictSessionRows(id, userID string, at time.Time) *sqlmock.Rows {
	return sqlmock.NewRows([]string{
		"id", "user_id", "material_id", "scene_type",
		"status", "duration_sec", "review_json", "created_at", "updated_at", "deleted_at",
	}).AddRow(
		id, userID, nil, "standup",
		StatusCreated, 0, []byte{}, at, at, nil,
	)
}

func TestMySQLStore_MarkSessionReviewedWithCost_AtomicOnEnded(t *testing.T) {
	store, mock, cleanup := newMySQLStoreMock(t)
	defer cleanup()

	sessionID := "sess-1"
	userID := "user-7"
	at := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	reviewJSON := []byte(`{"goal_achievement":{"met":true},"issues":[],"suggestions":[],"comparisons":[]}`)
	costLog := aicost.Log{
		ID:        "cost-1",
		UserID:    &userID,
		TaskType:  arkReviewTaskType,
		Model:     "ep-review",
		TokensIn:  1000,
		TokensOut: 2000,
		CostFen:   9,
		CreatedAt: at,
	}

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT ` + sessionColumnsRE + ` FROM practice_sessions WHERE id = \? FOR UPDATE`).
		WithArgs(sessionID).
		WillReturnRows(endedSessionRows(sessionID, userID, at))
	mock.ExpectExec(`UPDATE practice_sessions\s+SET status = \?, review_json = \?, updated_at = \?\s+WHERE id = \?`).
		WithArgs(StatusReviewed, reviewJSON, at, sessionID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO ai_cost_logs`).
		WithArgs(
			costLog.ID,                      // string
			aicostUserIDArg(costLog.UserID), // nullableString → nil or trimmed string
			costLog.TaskType,
			costLog.Model,
			costLog.TokensIn,
			costLog.TokensOut,
			costLog.AudioSec,
			costLog.CostFen,
			costLog.CreatedAt,
		).
		WillReturnResult(sqlmock.NewResult(1, 1))
	mock.ExpectCommit()

	got, err := store.MarkSessionReviewedWithCost(context.Background(), sessionID, reviewJSON, at, costLog)
	if err != nil {
		t.Fatalf("MarkSessionReviewedWithCost: %v", err)
	}
	if got.Status != StatusReviewed {
		t.Fatalf("status = %q, want reviewed", got.Status)
	}
	if !bytesContains(got.ReviewJSON, []byte(`"met":true`)) {
		t.Fatalf("review_json not committed: %s", got.ReviewJSON)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestMySQLStore_MarkSessionReviewedWithCost_RollsBackOnCostInsertFailure(t *testing.T) {
	store, mock, cleanup := newMySQLStoreMock(t)
	defer cleanup()

	sessionID := "sess-1"
	userID := "user-7"
	at := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	reviewJSON := []byte(`{"goal_achievement":{"met":false}}`)
	costLog := aicost.Log{
		ID:       "cost-2",
		UserID:   &userID,
		TaskType: arkReviewTaskType,
		TokensIn: 10, TokensOut: 20,
		CreatedAt: at,
	}

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT ` + sessionColumnsRE + ` FROM practice_sessions WHERE id = \? FOR UPDATE`).
		WithArgs(sessionID).
		WillReturnRows(endedSessionRows(sessionID, userID, at))
	mock.ExpectExec(`UPDATE practice_sessions`).
		WithArgs(StatusReviewed, reviewJSON, at, sessionID).
		WillReturnResult(sqlmock.NewResult(0, 1))
	mock.ExpectExec(`INSERT INTO ai_cost_logs`).
		WithArgs(
			costLog.ID,
			aicostUserIDArg(costLog.UserID),
			costLog.TaskType,
			costLog.Model,
			costLog.TokensIn,
			costLog.TokensOut,
			costLog.AudioSec,
			costLog.CostFen,
			costLog.CreatedAt,
		).
		WillReturnError(errors.New("cost insert failed: ai_cost_logs.user_id FK violation"))
	mock.ExpectRollback()

	_, err := store.MarkSessionReviewedWithCost(context.Background(), sessionID, reviewJSON, at, costLog)
	if err == nil {
		t.Fatal("expected error from cost insert failure")
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestMySQLStore_MarkSessionReviewedWithCost_IdempotentNoDoubleBill(t *testing.T) {
	store, mock, cleanup := newMySQLStoreMock(t)
	defer cleanup()

	sessionID := "sess-1"
	userID := "user-7"
	at := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	costLog := aicost.Log{
		ID:       "cost-dup",
		UserID:   &userID,
		TaskType: arkReviewTaskType,
		TokensIn: 10, TokensOut: 20,
		CreatedAt: at,
	}

	// Idempotent retry: session is already reviewed.
	// Expected SQL: BeginTx, SELECT FOR UPDATE, Commit. No UPDATE, no INSERT.
	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT ` + sessionColumnsRE + ` FROM practice_sessions WHERE id = \? FOR UPDATE`).
		WithArgs(sessionID).
		WillReturnRows(reviewedSessionRows(sessionID, userID, at))
	mock.ExpectCommit()

	got, err := store.MarkSessionReviewedWithCost(context.Background(), sessionID, []byte(`{}`), at, costLog)
	if err != nil {
		t.Fatalf("MarkSessionReviewedWithCost (idempotent): %v", err)
	}
	if got.Status != StatusReviewed {
		t.Fatalf("status = %q, want reviewed", got.Status)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestMySQLStore_MarkSessionReviewedWithCost_RejectsNonEnded(t *testing.T) {
	store, mock, cleanup := newMySQLStoreMock(t)
	defer cleanup()

	sessionID := "sess-1"
	userID := "user-7"
	at := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	costLog := aicost.Log{
		ID:        "cost-x",
		TaskType:  arkReviewTaskType,
		CreatedAt: at,
	}

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT ` + sessionColumnsRE + ` FROM practice_sessions WHERE id = \? FOR UPDATE`).
		WithArgs(sessionID).
		WillReturnRows(conflictSessionRows(sessionID, userID, at))
	mock.ExpectRollback()

	_, err := store.MarkSessionReviewedWithCost(context.Background(), sessionID, []byte(`{}`), at, costLog)
	if !errors.Is(err, ErrConflict) {
		t.Fatalf("expected ErrConflict, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

func TestMySQLStore_MarkSessionReviewedWithCost_NotFound(t *testing.T) {
	store, mock, cleanup := newMySQLStoreMock(t)
	defer cleanup()

	sessionID := "missing"
	at := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	costLog := aicost.Log{ID: "cost-x", TaskType: arkReviewTaskType, CreatedAt: at}

	mock.ExpectBegin()
	mock.ExpectQuery(`SELECT ` + sessionColumnsRE + ` FROM practice_sessions WHERE id = \? FOR UPDATE`).
		WithArgs(sessionID).
		WillReturnError(sql.ErrNoRows)
	mock.ExpectRollback()

	_, err := store.MarkSessionReviewedWithCost(context.Background(), sessionID, []byte(`{}`), at, costLog)
	if !errors.Is(err, ErrNotFound) {
		t.Fatalf("expected ErrNotFound, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("expectations: %v", err)
	}
}

// Sanity check: keep sessionColumnCount aligned with the scanSession arity in
// mysql_store.go. If you add a column to scanSession, this test fails loud.
func TestSessionColumnCountMatchesScanSession(t *testing.T) {
	if sessionColumnCount != 10 {
		t.Fatalf("sessionColumnCount = %d, mysql_store.scanSession expects 10; update both", sessionColumnCount)
	}
}

// MarkSessionReviewedWithCost must refuse to run when costTx is not wired.
// This guards the atomic-write invariant: silently falling back to inline
// INSERT would duplicate SQL across two packages and re-introduce the dead
// code path that the RecordCostTx refactor removed.
func TestMySQLStore_MarkSessionReviewedWithCost_RejectsUnwiredCostTx(t *testing.T) {
	mockDB, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherRegexp))
	if err != nil {
		t.Fatalf("sqlmock.New: %v", err)
	}
	defer func() { _ = mockDB.Close() }()

	store := NewMySQLStore(mockDB) // no SetCostTx — costTx stays nil
	_, err = store.MarkSessionReviewedWithCost(
		context.Background(),
		"any-session",
		[]byte(`{}`),
		time.Now().UTC(),
		aicost.Log{ID: "x", TaskType: arkReviewTaskType, CreatedAt: time.Now().UTC()},
	)
	if err == nil {
		t.Fatal("expected error when costTx is not wired")
	}
	if !strings.Contains(err.Error(), "costTx not wired") {
		t.Fatalf("expected error mentioning costTx wiring, got %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("no SQL should have been issued, got unmet expectations: %v", err)
	}
}

func TestMySQLStore_SaveUtteranceEval(t *testing.T) {
	store, mock, cleanup := newMySQLStoreMock(t)
	defer cleanup()

	raw := []byte(`{"score":0.5}`)
	mock.ExpectExec(regexp.QuoteMeta("UPDATE utterances SET llm_eval_json = ? WHERE id = ?")).
		WithArgs(raw, "utt-1").
		WillReturnResult(sqlmock.NewResult(0, 1))

	if err := store.SaveUtteranceEval(context.Background(), "utt-1", raw); err != nil {
		t.Fatalf("SaveUtteranceEval: %v", err)
	}
	if err := mock.ExpectationsWereMet(); err != nil {
		t.Fatalf("unmet expectations: %v", err)
	}

	mock.ExpectExec(regexp.QuoteMeta("UPDATE utterances SET llm_eval_json = ? WHERE id = ?")).
		WithArgs(raw, "missing").
		WillReturnResult(sqlmock.NewResult(0, 0))
	if err := store.SaveUtteranceEval(context.Background(), "missing", raw); !errors.Is(err, ErrNotFound) {
		t.Fatalf("missing utterance err = %v", err)
	}
}
