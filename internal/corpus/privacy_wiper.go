package corpus

import (
	"context"
	"time"
)

// PrivacyWiper soft-deletes the corpus for A4: the blocks and the quality
// signals the user left on them. Both are the user's own judgement, so they
// leave together under one entity.
type PrivacyWiper struct {
	Store Store
}

// EntityType implements account.DataWiper.
func (PrivacyWiper) EntityType() string { return "phrase_blocks" }

// Wipe implements account.DataWiper.
func (w PrivacyWiper) Wipe(ctx context.Context, userID string, at time.Time) (int, error) {
	if w.Store == nil {
		return 0, nil
	}
	blocks, err := w.Store.SoftDeleteAllForUser(ctx, userID, at)
	if err != nil {
		return blocks, err
	}
	feedback, err := w.Store.SoftDeleteFeedbackForUser(ctx, userID, at)
	return blocks + feedback, err
}

// Restore implements account.DataWiper.
func (w PrivacyWiper) Restore(ctx context.Context, userID string) (int, error) {
	if w.Store == nil {
		return 0, nil
	}
	blocks, err := w.Store.RestoreDeletedForUser(ctx, userID)
	if err != nil {
		return blocks, err
	}
	feedback, err := w.Store.RestoreFeedbackForUser(ctx, userID)
	return blocks + feedback, err
}
