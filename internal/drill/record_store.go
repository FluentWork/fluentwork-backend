package drill

import (
	"context"
	"sync"
	"time"
)

// RecordStore persists drill_records.
type RecordStore interface {
	Insert(ctx context.Context, rec Record) error
	DeleteForUser(ctx context.Context, userID string) (int, error)
}

// MemoryRecordStore is the local/dev ledger.
type MemoryRecordStore struct {
	mu      sync.Mutex
	nextID  int64
	records []Record
}

// NewMemoryRecordStore constructs an empty ledger.
func NewMemoryRecordStore() *MemoryRecordStore {
	return &MemoryRecordStore{records: make([]Record, 0)}
}

// Insert implements RecordStore.
func (s *MemoryRecordStore) Insert(_ context.Context, rec Record) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	rec.ID = s.nextID
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = time.Now().UTC()
	}
	s.records = append(s.records, rec)
	return nil
}

// DeleteForUser implements RecordStore.
func (s *MemoryRecordStore) DeleteForUser(_ context.Context, userID string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	kept := s.records[:0]
	n := 0
	for _, rec := range s.records {
		if rec.UserID == userID {
			n++
			continue
		}
		kept = append(kept, rec)
	}
	s.records = kept
	return n, nil
}

// Records returns a copy for tests.
func (s *MemoryRecordStore) Records() []Record {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Record, len(s.records))
	copy(out, s.records)
	return out
}
