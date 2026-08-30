package aicost

import (
	"context"
	"database/sql"
	"strings"
)

// MySQLStore persists ai_cost_logs in MySQL 8.
type MySQLStore struct {
	db *sql.DB
}

// NewMySQLStore constructs a MySQL-backed cost log store.
func NewMySQLStore(db *sql.DB) *MySQLStore {
	return &MySQLStore{db: db}
}

// Ping implements Store.
func (s *MySQLStore) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

// CreateLog implements Store.
func (s *MySQLStore) CreateLog(ctx context.Context, log Log) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO ai_cost_logs (
			id, user_id, task_type, model, tokens_in, tokens_out, audio_sec, cost_fen, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, log.ID, nullableString(log.UserID), log.TaskType, log.Model, log.TokensIn, log.TokensOut, log.AudioSec, log.CostFen, log.CreatedAt)
	return err
}

// ListRecent implements Store.
func (s *MySQLStore) ListRecent(ctx context.Context, userID string, limit int) ([]Log, error) {
	if limit <= 0 {
		limit = 50
	}
	if limit > 500 {
		limit = 500
	}

	query := `
		SELECT id, user_id, task_type, model, tokens_in, tokens_out, audio_sec, cost_fen, created_at
		FROM ai_cost_logs
	`
	args := []any{}
	if trimmed := strings.TrimSpace(userID); trimmed != "" {
		query += ` WHERE user_id = ?`
		args = append(args, trimmed)
	}
	query += ` ORDER BY created_at DESC LIMIT ?`
	args = append(args, limit)

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	out := make([]Log, 0, limit)
	for rows.Next() {
		log, err := scanLog(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, log)
	}
	if err := rows.Err(); err != nil {
		return nil, err
	}

	// Reorder oldest -> newest to match MemoryStore behavior.
	for i, j := 0, len(out)-1; i < j; i, j = i+1, j-1 {
		out[i], out[j] = out[j], out[i]
	}
	return out, nil
}

func scanLog(scanner interface{ Scan(dest ...any) error }) (Log, error) {
	var (
		log    Log
		userID sql.NullString
	)
	if err := scanner.Scan(
		&log.ID,
		&userID,
		&log.TaskType,
		&log.Model,
		&log.TokensIn,
		&log.TokensOut,
		&log.AudioSec,
		&log.CostFen,
		&log.CreatedAt,
	); err != nil {
		return Log{}, err
	}
	if userID.Valid {
		value := userID.String
		log.UserID = &value
	}
	return log, nil
}

func nullableString(value *string) any {
	if value == nil || strings.TrimSpace(*value) == "" {
		return nil
	}
	return strings.TrimSpace(*value)
}
