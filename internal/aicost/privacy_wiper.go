package aicost

import (
	"context"
	"time"
)

type costPrivacyStore interface {
	AnonymizeUser(ctx context.Context, userID string) (int, error)
	RestoreUser(ctx context.Context, userID string) (int, error)
}

// PrivacyWiper anonymizes ai_cost_logs.user_id for A4.
type PrivacyWiper struct {
	Store Store
}

// EntityType implements account.DataWiper.
func (PrivacyWiper) EntityType() string { return "ai_cost_logs" }

// Wipe implements account.DataWiper.
func (w PrivacyWiper) Wipe(ctx context.Context, userID string, _ time.Time) (int, error) {
	s, ok := w.Store.(costPrivacyStore)
	if !ok {
		return 0, nil
	}
	return s.AnonymizeUser(ctx, userID)
}

// Restore implements account.DataWiper.
func (w PrivacyWiper) Restore(ctx context.Context, userID string) (int, error) {
	s, ok := w.Store.(costPrivacyStore)
	if !ok {
		return 0, nil
	}
	return s.RestoreUser(ctx, userID)
}
