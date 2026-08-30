package aicost

import (
	"context"
	"slices"
	"strings"
	"sync"
)

// MemoryStore keeps ai_cost_logs in memory for local development and tests.
type MemoryStore struct {
	mu   sync.RWMutex
	logs []Log
}

// NewMemoryStore constructs an empty in-memory cost log store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		logs: make([]Log, 0, 32),
	}
}

// Ping implements Store.
func (s *MemoryStore) Ping(context.Context) error { return nil }

// CreateLog implements Store.
func (s *MemoryStore) CreateLog(_ context.Context, log Log) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.logs = append(s.logs, cloneLog(log))
	return nil
}

// ListRecent implements Store.
func (s *MemoryStore) ListRecent(_ context.Context, userID string, limit int) ([]Log, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()

	filtered := make([]Log, 0, len(s.logs))
	wantUser := strings.TrimSpace(userID)
	for i := len(s.logs) - 1; i >= 0; i-- {
		log := s.logs[i]
		if wantUser != "" {
			if log.UserID == nil || *log.UserID != wantUser {
				continue
			}
		}
		filtered = append(filtered, cloneLog(log))
		if limit > 0 && len(filtered) >= limit {
			break
		}
	}
	slices.Reverse(filtered)
	return filtered, nil
}

func cloneLog(log Log) Log {
	cloned := log
	if log.UserID != nil {
		copied := *log.UserID
		cloned.UserID = &copied
	}
	return cloned
}
