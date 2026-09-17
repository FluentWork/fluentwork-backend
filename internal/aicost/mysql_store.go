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
			id, user_id, task_type, model, tokens_in, tokens_out, audio_sec, chars, cost_micro_yuan, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, log.ID, nullableString(log.UserID), log.TaskType, log.Model, log.TokensIn, log.TokensOut, log.AudioSec, log.Chars, log.CostMicroYuan, log.CreatedAt)
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
		SELECT id, user_id, task_type, model, tokens_in, tokens_out, audio_sec, chars, cost_micro_yuan, created_at
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
		&log.Chars,
		&log.CostMicroYuan,
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

// RecordCostTx implements Store. It inserts one cost ledger row within an external
// database transaction. The caller is responsible for committing or rolling back tx.
func (s *MySQLStore) RecordCostTx(ctx context.Context, tx any, log Log) error {
	dbTx, ok := tx.(*sql.Tx)
	if !ok {
		return nil // not a *sql.Tx — caller must handle separately
	}
	_, err := dbTx.ExecContext(ctx, `
		INSERT INTO ai_cost_logs (
			id, user_id, task_type, model, tokens_in, tokens_out, audio_sec, chars, cost_micro_yuan, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, log.ID, nullableString(log.UserID), log.TaskType, log.Model, log.TokensIn, log.TokensOut, log.AudioSec, log.Chars, log.CostMicroYuan, log.CreatedAt)
	return err
}

// SummarizeCosts implements Store. The key expression is chosen from a closed
// set rather than interpolated from input, and the column list is fixed.
func (s *MySQLStore) SummarizeCosts(ctx context.Context, filter SummaryFilter) ([]SummaryRow, error) {
	keyExpr := "task_type"
	switch filter.GroupBy {
	case GroupByModel:
		keyExpr = "model"
	case GroupByDay:
		keyExpr = "DATE(created_at)"
	}
	query := `
		SELECT ` + keyExpr + ` AS bucket, COUNT(*),
		       COALESCE(SUM(tokens_in),0), COALESCE(SUM(tokens_out),0),
		       COALESCE(SUM(audio_sec),0), COALESCE(SUM(chars),0), COALESCE(SUM(cost_micro_yuan),0)
		FROM ai_cost_logs
		WHERE created_at >= ? AND created_at < ?`
	args := []any{filter.Since.UTC(), filter.Until.UTC()}
	if filter.UserID != "" {
		query += ` AND user_id = ?`
		args = append(args, filter.UserID)
	}
	query += ` GROUP BY bucket ORDER BY bucket ASC`

	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]SummaryRow, 0, 16)
	for rows.Next() {
		var (
			row  SummaryRow
			key  sql.NullString
			date sql.NullTime
		)
		dest := []any{&key, &row.Rows, &row.TokensIn, &row.TokensOut, &row.AudioSec, &row.Chars, &row.CostMicroYuan}
		if filter.GroupBy == GroupByDay {
			dest[0] = &date
		}
		if err := rows.Scan(dest...); err != nil {
			return nil, err
		}
		switch {
		case filter.GroupBy == GroupByDay && date.Valid:
			row.Key = date.Time.UTC().Format("2006-01-02")
		default:
			row.Key = key.String
		}
		out = append(out, row)
	}
	return out, rows.Err()
}

// AnonymizeUser copies user_id into user_id_anonymized and nulls user_id.
func (s *MySQLStore) AnonymizeUser(ctx context.Context, userID string) (int, error) {
	result, err := s.db.ExecContext(ctx, `
		UPDATE ai_cost_logs
		SET user_id_anonymized = user_id, user_id = NULL
		WHERE user_id = ?
	`, userID)
	if err != nil {
		return 0, err
	}
	n, err := result.RowsAffected()
	return int(n), err
}

// RestoreUser restores user_id from user_id_anonymized.
func (s *MySQLStore) RestoreUser(ctx context.Context, userID string) (int, error) {
	result, err := s.db.ExecContext(ctx, `
		UPDATE ai_cost_logs
		SET user_id = user_id_anonymized, user_id_anonymized = NULL
		WHERE user_id_anonymized = ?
	`, userID)
	if err != nil {
		return 0, err
	}
	n, err := result.RowsAffected()
	return int(n), err
}

func nullableString(value *string) any {
	if value == nil || strings.TrimSpace(*value) == "" {
		return nil
	}
	return strings.TrimSpace(*value)
}
