package session

import (
	"context"
	"database/sql"
	"errors"
	"time"
)

// MySQLStore persists sessions and tickets in MySQL 8.
type MySQLStore struct {
	db *sql.DB
}

// NewMySQLStore wraps an opened database handle.
func NewMySQLStore(db *sql.DB) *MySQLStore {
	return &MySQLStore{db: db}
}

// Ping verifies database connectivity.
func (s *MySQLStore) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

const sessionColumns = `id, user_id, material_id, scene_type, status, duration_sec, created_at, updated_at`

// CreateSession inserts a practice session row.
func (s *MySQLStore) CreateSession(ctx context.Context, session Session) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO practice_sessions (
			id, user_id, material_id, scene_type, status, duration_sec, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, session.ID, session.UserID, nullString(session.MaterialID), session.SceneType, session.Status,
		session.DurationSec, session.CreatedAt, session.UpdatedAt)
	return err
}

// GetSession returns a session by id.
func (s *MySQLStore) GetSession(ctx context.Context, id string) (Session, error) {
	return scanSession(s.db.QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM practice_sessions WHERE id = ?`, id))
}

// CreateTicket inserts a one-time WSS ticket row.
func (s *MySQLStore) CreateTicket(ctx context.Context, ticket Ticket) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO session_tickets (
			id, session_id, user_id, token_hash, expires_at, used_at, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)
	`, ticket.ID, ticket.SessionID, ticket.UserID, ticket.Hash, ticket.ExpiresAt, nullTime(ticket.UsedAt), ticket.CreatedAt)
	return err
}

// GetTicketByHash returns a ticket by hashed raw value.
func (s *MySQLStore) GetTicketByHash(ctx context.Context, hash string) (Ticket, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, session_id, user_id, token_hash, expires_at, used_at, created_at
		FROM session_tickets
		WHERE token_hash = ?
	`, hash)
	return scanTicket(row)
}

// ReassignUser moves sessions and tickets from a guest onto a registered account.
func (s *MySQLStore) ReassignUser(ctx context.Context, fromUserID, toUserID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `
		UPDATE practice_sessions SET user_id = ?, updated_at = ? WHERE user_id = ?
	`, toUserID, now, fromUserID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE session_tickets SET user_id = ? WHERE user_id = ?
	`, toUserID, fromUserID); err != nil {
		return err
	}
	return tx.Commit()
}

func scanSession(row *sql.Row) (Session, error) {
	var session Session
	var materialID sql.NullString
	err := row.Scan(
		&session.ID,
		&session.UserID,
		&materialID,
		&session.SceneType,
		&session.Status,
		&session.DurationSec,
		&session.CreatedAt,
		&session.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, err
	}
	if materialID.Valid {
		value := materialID.String
		session.MaterialID = &value
	}
	return session, nil
}

func scanTicket(row *sql.Row) (Ticket, error) {
	var ticket Ticket
	var usedAt sql.NullTime
	err := row.Scan(
		&ticket.ID,
		&ticket.SessionID,
		&ticket.UserID,
		&ticket.Hash,
		&ticket.ExpiresAt,
		&usedAt,
		&ticket.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Ticket{}, ErrNotFound
	}
	if err != nil {
		return Ticket{}, err
	}
	if usedAt.Valid {
		value := usedAt.Time
		ticket.UsedAt = &value
	}
	return ticket, nil
}

func nullString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return *value
}
