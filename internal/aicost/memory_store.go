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

// SummarizeCosts implements Store.
func (s *MemoryStore) SummarizeCosts(_ context.Context, filter SummaryFilter) ([]SummaryRow, error) {
	s.mu.RLock()
	defer s.mu.RUnlock()
	buckets := map[string]*SummaryRow{}
	order := make([]string, 0, 8)
	for _, log := range s.logs {
		if filter.UserID != "" {
			if log.UserID == nil || *log.UserID != filter.UserID {
				continue
			}
		}
		at := log.CreatedAt.UTC()
		if at.Before(filter.Since) || !at.Before(filter.Until) {
			continue
		}
		key := summaryKey(log, filter.GroupBy)
		row, ok := buckets[key]
		if !ok {
			row = &SummaryRow{Key: key}
			buckets[key] = row
			order = append(order, key)
		}
		row.Rows++
		row.TokensIn += log.TokensIn
		row.TokensOut += log.TokensOut
		row.AudioSec += log.AudioSec
		row.Chars += log.Chars
		row.CostMicroYuan += log.CostMicroYuan
	}
	slices.Sort(order)
	out := make([]SummaryRow, 0, len(order))
	for _, key := range order {
		out = append(out, *buckets[key])
	}
	return out, nil
}

// summaryKey buckets one row. Day keys are UTC dates, the same calendar the
// scheduler and the drill windows use.
func summaryKey(log Log, groupBy string) string {
	switch groupBy {
	case GroupByModel:
		return log.Model
	case GroupByDay:
		return log.CreatedAt.UTC().Format("2006-01-02")
	default:
		return log.TaskType
	}
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

// RecordCostTx implements Store. MemoryStore cannot participate in external transactions,
// so this is a no-op — callers must handle the nil error case when using the
// memory store in tests.
func (*MemoryStore) RecordCostTx(_ context.Context, _ any, _ Log) error {
	return nil // no-op for in-memory store
}

// AnonymizeUser nulls user_id and stores the original in UserIDAnonymized.
func (s *MemoryStore) AnonymizeUser(_ context.Context, userID string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for i, log := range s.logs {
		if log.UserID == nil || *log.UserID != userID {
			continue
		}
		orig := *log.UserID
		s.logs[i].UserIDAnonymized = &orig
		s.logs[i].UserID = nil
		n++
	}
	return n, nil
}

// RestoreUser moves user_id_anonymized back onto user_id.
func (s *MemoryStore) RestoreUser(_ context.Context, userID string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for i, log := range s.logs {
		if log.UserIDAnonymized == nil || *log.UserIDAnonymized != userID {
			continue
		}
		copied := *log.UserIDAnonymized
		s.logs[i].UserID = &copied
		s.logs[i].UserIDAnonymized = nil
		n++
	}
	return n, nil
}

func cloneLog(log Log) Log {
	cloned := log
	if log.UserID != nil {
		copied := *log.UserID
		cloned.UserID = &copied
	}
	if log.UserIDAnonymized != nil {
		copied := *log.UserIDAnonymized
		cloned.UserIDAnonymized = &copied
	}
	return cloned
}
