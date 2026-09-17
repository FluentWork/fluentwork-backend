package drill

import (
	"context"
	"database/sql"
	"fmt"
	"log/slog"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql" // registers the mysql driver used by OpenRecordStore

	"github.com/FluentWork/fluentwork-backend/internal/config"
	"github.com/FluentWork/fluentwork-backend/internal/corpus"
)

// MySQLRecordStore persists drill_records.
type MySQLRecordStore struct {
	db *sql.DB
}

// NewMySQLRecordStore wraps an opened database handle.
func NewMySQLRecordStore(db *sql.DB) *MySQLRecordStore {
	return &MySQLRecordStore{db: db}
}

const recordColumns = `id, user_id, block_id, session_id, drill_type, semantic_pass, response_ms, asr_text, judge_reason, prev_state, prev_success_streak, prev_next_due_at, appealed_at, created_at`

// Insert implements RecordStore.
func (s *MySQLRecordStore) Insert(ctx context.Context, rec Record) (int64, error) {
	result, err := s.db.ExecContext(ctx, `
		INSERT INTO drill_records (
			user_id, block_id, session_id, drill_type, semantic_pass, response_ms, asr_text, judge_reason,
			prev_state, prev_success_streak, prev_next_due_at, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, rec.UserID, rec.BlockID, nullSessionID(rec.SessionID), rec.DrillType, rec.SemanticPass, rec.ResponseMS,
		rec.ASRText, rec.JudgeReason, rec.PrevState, rec.PrevSuccessStreak,
		nullTime(rec.PrevNextDueAt), rec.CreatedAt.UTC())
	if err != nil {
		return 0, err
	}
	id, err := result.LastInsertId()
	if err != nil {
		return 0, err
	}
	return id, nil
}

// GetRecord implements RecordStore.
func (s *MySQLRecordStore) GetRecord(ctx context.Context, userID string, recordID int64) (Record, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT `+recordColumns+`
		FROM drill_records
		WHERE id = ? AND user_id = ?
	`, recordID, userID)
	rec, err := scanRecord(row)
	if err != nil {
		if err == sql.ErrNoRows {
			return Record{}, ErrRecordNotFound
		}
		return Record{}, err
	}
	return rec, nil
}

// MarkAppealed implements RecordStore.
func (s *MySQLRecordStore) MarkAppealed(ctx context.Context, userID string, recordID int64, at time.Time) (bool, error) {
	result, err := s.db.ExecContext(ctx, `
		UPDATE drill_records
		SET appealed_at = ?
		WHERE id = ? AND user_id = ? AND appealed_at IS NULL
	`, at.UTC(), recordID, userID)
	if err != nil {
		return false, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return false, err
	}
	if n == 1 {
		return true, nil
	}
	// Nothing updated: either it was already appealed, or it is not this
	// user's record. Distinguish so the caller can answer 404 rather than
	// silently reporting an idempotent success.
	if _, err := s.GetRecord(ctx, userID, recordID); err != nil {
		return false, err
	}
	return false, nil
}

// IsLatestForBlock implements RecordStore.
func (s *MySQLRecordStore) IsLatestForBlock(ctx context.Context, userID, blockID string, recordID int64) (bool, error) {
	var latest sql.NullInt64
	err := s.db.QueryRowContext(ctx, `
		SELECT MAX(id) FROM drill_records WHERE user_id = ? AND block_id = ?
	`, userID, blockID).Scan(&latest)
	if err != nil {
		return false, err
	}
	if !latest.Valid {
		return false, ErrRecordNotFound
	}
	return latest.Int64 == recordID, nil
}

// CountNewReleasesSince implements RecordStore.
func (s *MySQLRecordStore) CountNewReleasesSince(ctx context.Context, userID string, since time.Time) (int, error) {
	var n int
	err := s.db.QueryRowContext(ctx, `
		SELECT COUNT(*) FROM drill_records
		WHERE user_id = ? AND prev_state = ? AND created_at >= ?
	`, userID, corpus.StateNew, since.UTC()).Scan(&n)
	if err != nil {
		return 0, err
	}
	return n, nil
}

type recordScanner interface {
	Scan(dest ...any) error
}

func scanRecord(row recordScanner) (Record, error) {
	var (
		rec       Record
		sessionID sql.NullString
		reason    sql.NullString
		dueAt     sql.NullTime
		appealed  sql.NullTime
	)
	if err := row.Scan(
		&rec.ID, &rec.UserID, &rec.BlockID, &sessionID, &rec.DrillType, &rec.SemanticPass,
		&rec.ResponseMS, &rec.ASRText, &reason, &rec.PrevState, &rec.PrevSuccessStreak,
		&dueAt, &appealed, &rec.CreatedAt,
	); err != nil {
		return Record{}, err
	}
	rec.SessionID = sessionID.String
	rec.JudgeReason = reason.String
	if dueAt.Valid {
		rec.PrevNextDueAt = dueAt.Time
	}
	if appealed.Valid {
		at := appealed.Time
		rec.AppealedAt = &at
	}
	return rec, nil
}

func nullTime(t time.Time) any {
	if t.IsZero() {
		return nil
	}
	return t.UTC()
}

// DeleteForUser implements RecordStore.
func (s *MySQLRecordStore) DeleteForUser(ctx context.Context, userID string) (int, error) {
	result, err := s.db.ExecContext(ctx, `DELETE FROM drill_records WHERE user_id = ?`, userID)
	if err != nil {
		return 0, err
	}
	n, err := result.RowsAffected()
	return int(n), err
}

func nullSessionID(id string) any {
	id = strings.TrimSpace(id)
	if id == "" {
		return nil
	}
	return id
}

// OpenRecordStore returns a MySQL ledger when MYSQL_DSN is set, otherwise memory.
func OpenRecordStore(cfg config.Config, logger *slog.Logger) (RecordStore, func() error, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.MySQLDSN == "" {
		logger.Warn("MYSQL_DSN is empty; using in-memory drill record store")
		return NewMemoryRecordStore(), func() error { return nil }, nil
	}
	db, err := sql.Open("mysql", ensureParseTime(cfg.MySQLDSN))
	if err != nil {
		return nil, nil, fmt.Errorf("open mysql: %w", err)
	}
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(30 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("ping mysql: %w", err)
	}
	return NewMySQLRecordStore(db), db.Close, nil
}

func ensureParseTime(dsn string) string {
	lower := strings.ToLower(dsn)
	if !strings.Contains(lower, "parsetime=") {
		if strings.Contains(dsn, "?") {
			dsn += "&parseTime=true"
		} else {
			dsn += "?parseTime=true"
		}
	}
	if !strings.Contains(strings.ToLower(dsn), "loc=") {
		if strings.Contains(dsn, "?") {
			dsn += "&loc=UTC"
		} else {
			dsn += "?loc=UTC"
		}
	}
	return dsn
}
