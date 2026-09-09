package drill

import "context"

// RecordWiper hard-deletes drill_records for A4. Records are not restored.
type RecordWiper struct {
	Store RecordStore
}

// EntityType identifies drill_records in cascaded counts.
func (RecordWiper) EntityType() string { return "drill_records" }

// Delete implements account.HardDeleter.
func (w RecordWiper) Delete(ctx context.Context, userID string) (int, error) {
	if w.Store == nil {
		return 0, nil
	}
	return w.Store.DeleteForUser(ctx, userID)
}
