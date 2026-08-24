package account

import (
	"context"
	"database/sql"
	"errors"
	"strings"
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
)

// MySQLStore persists account state in MySQL 8.
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

const userColumns = `id, email, phone, device_id, is_guest, pwd_hash, status, merged_into_user_id, created_at, updated_at`

// CreateUser inserts a user row.
func (s *MySQLStore) CreateUser(ctx context.Context, user User) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO users (
			id, email, phone, device_id, is_guest, pwd_hash, status, merged_into_user_id, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, user.ID, nullString(user.Email), nullString(user.Phone), nullString(user.DeviceID), user.IsGuest,
		nullString(user.PasswordHash), user.Status, nullString(user.MergedIntoUserID), user.CreatedAt, user.UpdatedAt)
	if err != nil {
		if isMySQLDuplicateDeviceID(err) {
			return ErrDuplicateDeviceID
		}
		return err
	}
	return nil
}

// GetUser returns a user by id.
func (s *MySQLStore) GetUser(ctx context.Context, id string) (User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `SELECT `+userColumns+` FROM users WHERE id = ?`, id))
}

// GetActiveByDeviceID returns the active user currently bound to device_id.
func (s *MySQLStore) GetActiveByDeviceID(ctx context.Context, deviceID string) (User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `
		SELECT `+userColumns+`
		FROM users
		WHERE device_id = ? AND status = ?
		LIMIT 1
	`, deviceID, UserStatusActive))
}

// MarkMerged transfers device_id onto the registered user and archives the guest.
func (s *MySQLStore) MarkMerged(ctx context.Context, guestID, targetID, deviceID string, at time.Time) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	result, err := tx.ExecContext(ctx, `
		UPDATE users
		SET device_id = NULL, status = ?, merged_into_user_id = ?, updated_at = ?
		WHERE id = ? AND status = ? AND is_guest = 1
	`, UserStatusMerged, targetID, at, guestID, UserStatusActive)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrNotFound
	}
	result, err = tx.ExecContext(ctx, `
		UPDATE users SET device_id = ?, updated_at = ? WHERE id = ? AND status = ?
	`, deviceID, at, targetID, UserStatusActive)
	if err != nil {
		return err
	}
	n, err = result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrNotFound
	}
	return tx.Commit()
}

// FindGuestMergedInto returns the most recently merged guest for a target user.
func (s *MySQLStore) FindGuestMergedInto(ctx context.Context, targetID string) (User, error) {
	return scanUser(s.db.QueryRowContext(ctx, `
		SELECT `+userColumns+`
		FROM users
		WHERE merged_into_user_id = ? AND status = ?
		ORDER BY updated_at DESC
		LIMIT 1
	`, targetID, UserStatusMerged))
}

// ReplaceRefreshToken drops existing refresh tokens for the user and stores one.
func (s *MySQLStore) ReplaceRefreshToken(ctx context.Context, token RefreshToken) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()
	var lockedID string
	if err := tx.QueryRowContext(ctx, `SELECT id FROM users WHERE id = ? FOR UPDATE`, token.UserID).Scan(&lockedID); err != nil {
		if errors.Is(err, sql.ErrNoRows) {
			return ErrNotFound
		}
		return err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM auth_tokens WHERE user_id = ?`, token.UserID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO auth_tokens (id, user_id, refresh_token_hash, expires_at, created_at)
		VALUES (?, ?, ?, ?, ?)
	`, token.ID, token.UserID, token.Hash, token.ExpiresAt, token.CreatedAt); err != nil {
		return err
	}
	return tx.Commit()
}

// DeleteRefreshTokensForUser removes refresh tokens for one user.
func (s *MySQLStore) DeleteRefreshTokensForUser(ctx context.Context, userID string) error {
	_, err := s.db.ExecContext(ctx, `DELETE FROM auth_tokens WHERE user_id = ?`, userID)
	return err
}

type rowScanner interface {
	Scan(dest ...any) error
}

func scanUser(row rowScanner) (User, error) {
	var user User
	var email, phone, deviceID, pwdHash, mergedInto sql.NullString
	err := row.Scan(
		&user.ID,
		&email,
		&phone,
		&deviceID,
		&user.IsGuest,
		&pwdHash,
		&user.Status,
		&mergedInto,
		&user.CreatedAt,
		&user.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return User{}, ErrNotFound
	}
	if err != nil {
		return User{}, err
	}
	user.Email = ptrString(email)
	user.Phone = ptrString(phone)
	user.DeviceID = ptrString(deviceID)
	user.PasswordHash = ptrString(pwdHash)
	user.MergedIntoUserID = ptrString(mergedInto)
	return user, nil
}

func nullString(value *string) sql.NullString {
	if value == nil || *value == "" {
		return sql.NullString{}
	}
	return sql.NullString{String: *value, Valid: true}
}

func ptrString(value sql.NullString) *string {
	if !value.Valid {
		return nil
	}
	copied := value.String
	return &copied
}

func isMySQLDuplicateDeviceID(err error) bool {
	var mysqlErr *mysqldriver.MySQLError
	if !errors.As(err, &mysqlErr) || mysqlErr.Number != 1062 {
		return false
	}
	msg := strings.ToLower(mysqlErr.Message)
	return strings.Contains(msg, "uk_users_device_id") || strings.Contains(msg, "device_id")
}
