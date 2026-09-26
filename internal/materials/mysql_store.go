package materials

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// MySQLStore persists materials in MySQL 8.
type MySQLStore struct {
	db *sql.DB
}

// NewMySQLStore wraps an opened handle.
func NewMySQLStore(db *sql.DB) *MySQLStore {
	return &MySQLStore{db: db}
}

const materialColumns = `id, user_id, kind, content, refine_status, block_count, error_code, deleted_at, created_at, updated_at, attempts, locked_at`

// Ping verifies connectivity.
func (s *MySQLStore) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

// InsertMaterial inserts a row.
func (s *MySQLStore) InsertMaterial(ctx context.Context, m Material) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO materials (
			id, user_id, kind, content, refine_status, block_count, error_code, deleted_at, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, m.ID, m.UserID, m.Kind, m.Content, m.RefineStatus, m.BlockCount, nullString(m.ErrorCode), nullTime(m.DeletedAt), m.CreatedAt, m.UpdatedAt)
	return err
}

// GetMaterial loads by id.
func (s *MySQLStore) GetMaterial(ctx context.Context, _, materialID string) (Material, error) {
	return scanMaterial(s.db.QueryRowContext(ctx, `SELECT `+materialColumns+` FROM materials WHERE id = ?`, materialID))
}

// MarkProcessing is queued → processing, and starts the reclaim lease.
func (s *MySQLStore) MarkProcessing(ctx context.Context, materialID string, at time.Time) error {
	ts := at.UTC()
	result, err := s.db.ExecContext(ctx, `
		UPDATE materials
		SET refine_status = ?, attempts = attempts + 1, locked_at = ?, updated_at = ?
		WHERE id = ? AND refine_status = ?
	`, StatusProcessing, ts, ts, materialID, StatusQueued)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 1 {
		return nil
	}
	_, getErr := s.GetMaterial(ctx, "", materialID)
	if getErr != nil {
		return getErr
	}
	return ErrConflict
}

// MarkRefined is processing → ready.
func (s *MySQLStore) MarkRefined(ctx context.Context, materialID string, blockCount int, errorCode string, at time.Time) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE materials
		SET refine_status = ?, block_count = ?, error_code = ?, locked_at = NULL, updated_at = ?
		WHERE id = ? AND refine_status = ?
	`, StatusReady, blockCount, nullString(errorCode), at.UTC(), materialID, StatusProcessing)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 1 {
		return nil
	}
	return ErrConflict
}

// MarkRefineFailed is processing → failed.
func (s *MySQLStore) MarkRefineFailed(ctx context.Context, materialID, errorCode string, at time.Time) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE materials
		SET refine_status = ?, error_code = ?, locked_at = NULL, updated_at = ?
		WHERE id = ? AND refine_status = ?
	`, StatusFailed, nullString(errorCode), at.UTC(), materialID, StatusProcessing)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 1 {
		return nil
	}
	return ErrConflict
}

// ReclaimExpired requeues processing rows whose lease expired, and fails the
// ones that already used up their attempts.
func (s *MySQLStore) ReclaimExpired(ctx context.Context, at time.Time) (ReclaimResult, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return ReclaimResult{}, err
	}
	defer func() { _ = tx.Rollback() }()

	ts := at.UTC()
	rows, err := tx.QueryContext(ctx, `
		SELECT id, attempts FROM materials
		WHERE refine_status = ? AND locked_at IS NOT NULL AND locked_at <= ? AND deleted_at IS NULL
		ORDER BY locked_at ASC, id ASC
		FOR UPDATE
	`, StatusProcessing, ts.Add(-DefaultRefineLease))
	if err != nil {
		return ReclaimResult{}, err
	}
	type stuck struct {
		id       string
		attempts int
	}
	var found []stuck
	for rows.Next() {
		var it stuck
		if err := rows.Scan(&it.id, &it.attempts); err != nil {
			_ = rows.Close()
			return ReclaimResult{}, err
		}
		found = append(found, it)
	}
	if err := rows.Err(); err != nil {
		_ = rows.Close()
		return ReclaimResult{}, err
	}
	if err := rows.Close(); err != nil {
		return ReclaimResult{}, err
	}

	var out ReclaimResult
	for _, it := range found {
		status := StatusQueued
		errorCode := ""
		if it.attempts >= MaxRefineAttempts {
			status = StatusFailed
			errorCode = ErrorLeaseExpired
		} else {
			out.Requeued = append(out.Requeued, it.id)
		}
		if _, err := tx.ExecContext(ctx, `
			UPDATE materials
			SET refine_status = ?, error_code = ?, locked_at = NULL, updated_at = ?
			WHERE id = ? AND refine_status = ?
		`, status, nullString(errorCode), ts, it.id, StatusProcessing); err != nil {
			return ReclaimResult{}, err
		}
		if status == StatusFailed {
			out.Expired++
		}
	}
	if err := tx.Commit(); err != nil {
		return ReclaimResult{}, err
	}
	return out, nil
}

// SoftDeleteAllForUser sets deleted_at.
func (s *MySQLStore) SoftDeleteAllForUser(ctx context.Context, userID string, at time.Time) (int, error) {
	result, err := s.db.ExecContext(ctx, `
		UPDATE materials SET deleted_at = ?, updated_at = ?
		WHERE user_id = ? AND deleted_at IS NULL
	`, at.UTC(), at.UTC(), userID)
	if err != nil {
		return 0, err
	}
	n, err := result.RowsAffected()
	return int(n), err
}

// RestoreDeletedForUser clears deleted_at.
func (s *MySQLStore) RestoreDeletedForUser(ctx context.Context, userID string) (int, error) {
	result, err := s.db.ExecContext(ctx, `
		UPDATE materials SET deleted_at = NULL WHERE user_id = ? AND deleted_at IS NOT NULL
	`, userID)
	if err != nil {
		return 0, err
	}
	n, err := result.RowsAffected()
	return int(n), err
}

func scanMaterial(row *sql.Row) (Material, error) {
	var m Material
	var errCode sql.NullString
	var deleted sql.NullTime
	var lockedAt sql.NullTime
	err := row.Scan(&m.ID, &m.UserID, &m.Kind, &m.Content, &m.RefineStatus, &m.BlockCount, &errCode, &deleted, &m.CreatedAt, &m.UpdatedAt, &m.attempts, &lockedAt)
	if errors.Is(err, sql.ErrNoRows) {
		return Material{}, ErrNotFound
	}
	if err != nil {
		return Material{}, err
	}
	if errCode.Valid {
		m.ErrorCode = errCode.String
	}
	if deleted.Valid {
		t := deleted.Time.UTC()
		m.DeletedAt = &t
	}
	if lockedAt.Valid {
		t := lockedAt.Time.UTC()
		m.lockedAt = &t
	}
	return m, nil
}

func nullString(s string) any {
	if s == "" {
		return nil
	}
	return s
}

func nullTime(t *time.Time) any {
	if t == nil {
		return nil
	}
	return t.UTC()
}
