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
)

// MySQLRecordStore persists drill_records.
type MySQLRecordStore struct {
	db *sql.DB
}

// NewMySQLRecordStore wraps an opened database handle.
func NewMySQLRecordStore(db *sql.DB) *MySQLRecordStore {
	return &MySQLRecordStore{db: db}
}

// Insert implements RecordStore.
func (s *MySQLRecordStore) Insert(ctx context.Context, rec Record) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO drill_records (
			user_id, block_id, session_id, drill_type, semantic_pass, response_ms, asr_text, judge_reason, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, rec.UserID, rec.BlockID, nullSessionID(rec.SessionID), rec.DrillType, rec.SemanticPass, rec.ResponseMS, rec.ASRText, rec.JudgeReason, rec.CreatedAt.UTC())
	return err
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
