package drill

import (
	"context"
	"errors"
	"sort"
	"sync"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/corpus"
)

// ErrRecordNotFound is returned when an attempt does not exist or belongs to
// another user. The two cases are one answer on purpose: telling a caller that
// a record exists but is not theirs is itself the leak.
var ErrRecordNotFound = errors.New("drill: record not found")

// RecordStore persists drill_records.
type RecordStore interface {
	// Insert appends one attempt and returns its ledger ID, which the judge
	// response carries so E2's appeal can name the attempt it disputes.
	Insert(ctx context.Context, rec Record) (int64, error)
	DeleteForUser(ctx context.Context, userID string) (int, error)
	// GetRecord returns one attempt owned by userID, or ErrRecordNotFound.
	GetRecord(ctx context.Context, userID string, recordID int64) (Record, error)
	// MarkAppealed stamps appealed_at when it is still empty and reports
	// whether this call is the one that stamped it.
	MarkAppealed(ctx context.Context, userID string, recordID int64, at time.Time) (bool, error)
	// IsLatestForBlock reports whether recordID is the newest attempt on its
	// block: the guard that stops an old appeal from rolling back a later,
	// legitimate attempt (PRD §7.5 的申诉只对"被误判的那次"生效).
	IsLatestForBlock(ctx context.Context, userID, blockID string, recordID int64) (bool, error)
	// ListRecordsSince returns the user's attempts in a window, oldest first.
	// The stuck map groups them in Go so both stores rank by one rule.
	ListRecordsSince(ctx context.Context, userID string, since time.Time) ([]Record, error)
	// CountNewReleasesSince counts attempts since a time whose block was still
	// 灰 when it was served — the accounting behind 每日新块释放上限 (E3).
	// Attempts recorded before that snapshot column existed count as zero.
	CountNewReleasesSince(ctx context.Context, userID string, since time.Time) (int, error)
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
func (s *MemoryRecordStore) Insert(_ context.Context, rec Record) (int64, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.nextID++
	rec.ID = s.nextID
	if rec.CreatedAt.IsZero() {
		rec.CreatedAt = time.Now().UTC()
	}
	s.records = append(s.records, rec)
	return rec.ID, nil
}

// GetRecord implements RecordStore.
func (s *MemoryRecordStore) GetRecord(_ context.Context, userID string, recordID int64) (Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, rec := range s.records {
		if rec.ID == recordID && rec.UserID == userID {
			return rec, nil
		}
	}
	return Record{}, ErrRecordNotFound
}

// MarkAppealed implements RecordStore.
func (s *MemoryRecordStore) MarkAppealed(_ context.Context, userID string, recordID int64, at time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for i := range s.records {
		rec := &s.records[i]
		if rec.ID != recordID || rec.UserID != userID {
			continue
		}
		if rec.AppealedAt != nil {
			return false, nil
		}
		stamped := at.UTC()
		rec.AppealedAt = &stamped
		return true, nil
	}
	return false, ErrRecordNotFound
}

// ListRecordsSince implements RecordStore.
func (s *MemoryRecordStore) ListRecordsSince(_ context.Context, userID string, since time.Time) ([]Record, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]Record, 0, len(s.records))
	for _, rec := range s.records {
		if rec.UserID != userID || rec.CreatedAt.Before(since) {
			continue
		}
		out = append(out, rec)
	}
	sort.Slice(out, func(i, j int) bool { return out[i].ID < out[j].ID })
	return out, nil
}

// CountNewReleasesSince implements RecordStore.
func (s *MemoryRecordStore) CountNewReleasesSince(_ context.Context, userID string, since time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for _, rec := range s.records {
		if rec.UserID != userID || rec.PrevState != corpus.StateNew {
			continue
		}
		if rec.CreatedAt.Before(since) {
			continue
		}
		n++
	}
	return n, nil
}

// IsLatestForBlock implements RecordStore.
func (s *MemoryRecordStore) IsLatestForBlock(_ context.Context, userID, blockID string, recordID int64) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var latest int64
	for _, rec := range s.records {
		if rec.UserID != userID || rec.BlockID != blockID {
			continue
		}
		if rec.ID > latest {
			latest = rec.ID
		}
	}
	if latest == 0 {
		return false, ErrRecordNotFound
	}
	return latest == recordID, nil
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
