package topic

import (
	"context"
	"time"
)

// PrivacyWiper soft-deletes topic cards for A4.
type PrivacyWiper struct {
	Store Store
}

// EntityType implements account.DataWiper.
func (PrivacyWiper) EntityType() string { return "topic_cards" }

// Wipe implements account.DataWiper.
func (w PrivacyWiper) Wipe(ctx context.Context, userID string, at time.Time) (int, error) {
	if w.Store == nil {
		return 0, nil
	}
	return w.Store.SoftDeleteAllForUser(ctx, userID, at)
}

// Restore implements account.DataWiper.
func (w PrivacyWiper) Restore(ctx context.Context, userID string) (int, error) {
	if w.Store == nil {
		return 0, nil
	}
	return w.Store.RestoreDeletedForUser(ctx, userID)
}
