package session

import (
	"context"
	"time"
)

type sessionPrivacyStore interface {
	SoftDeleteForUser(ctx context.Context, userID string, deletedAt time.Time) (int, error)
	RestoreDeletedForUser(ctx context.Context, userID string) (int, error)
}

// PrivacyWiper soft-deletes practice_sessions for A4.
type PrivacyWiper struct {
	Store Store
}

// EntityType implements account.DataWiper.
func (PrivacyWiper) EntityType() string { return "practice_sessions" }

// Wipe implements account.DataWiper.
func (w PrivacyWiper) Wipe(ctx context.Context, userID string, at time.Time) (int, error) {
	s, ok := w.Store.(sessionPrivacyStore)
	if !ok {
		return 0, nil
	}
	return s.SoftDeleteForUser(ctx, userID, at)
}

// Restore implements account.DataWiper.
func (w PrivacyWiper) Restore(ctx context.Context, userID string) (int, error) {
	s, ok := w.Store.(sessionPrivacyStore)
	if !ok {
		return 0, nil
	}
	return s.RestoreDeletedForUser(ctx, userID)
}
