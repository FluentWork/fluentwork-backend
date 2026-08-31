package content

import (
	"context"
	"encoding/json"
	"sync"
	"time"
)

// MemoryStore keeps daily reads in memory for local development and tests.
type MemoryStore struct {
	mu    sync.Mutex
	reads map[string]DailyRead
}

// NewMemoryStore constructs an in-memory content store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{reads: make(map[string]DailyRead)}
}

// Ping implements Store.
func (s *MemoryStore) Ping(context.Context) error {
	return nil
}

// GetByUserDate implements Store.
func (s *MemoryStore) GetByUserDate(_ context.Context, userID string, genDate time.Time) (DailyRead, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := userDateKey(userID, genDate)
	for _, read := range s.reads {
		if userDateKey(read.UserID, read.GenDate) == key {
			return cloneRead(read), nil
		}
	}
	return DailyRead{}, ErrNotFound
}

// CreatePending implements Store.
func (s *MemoryStore) CreatePending(_ context.Context, read DailyRead) (DailyRead, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	key := userDateKey(read.UserID, read.GenDate)
	for _, existing := range s.reads {
		if userDateKey(existing.UserID, existing.GenDate) == key {
			return cloneRead(existing), nil
		}
	}
	s.reads[read.ID] = cloneRead(read)
	return cloneRead(read), nil
}

// MarkReady implements Store.
func (s *MemoryStore) MarkReady(_ context.Context, read DailyRead) (DailyRead, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.reads[read.ID]
	if !ok {
		return DailyRead{}, ErrNotFound
	}
	current.Status = StatusReady
	current.Title = read.Title
	current.Body = read.Body
	current.AudioURL = cloneStringPtr(read.AudioURL)
	current.UsedBlockIDs = append(json.RawMessage(nil), read.UsedBlockIDs...)
	current.SourceRefs = append(json.RawMessage(nil), read.SourceRefs...)
	current.Generator = read.Generator
	current.UpdatedAt = read.UpdatedAt
	s.reads[read.ID] = current
	return cloneRead(current), nil
}

// MarkFailed implements Store.
func (s *MemoryStore) MarkFailed(_ context.Context, id string, updatedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	current, ok := s.reads[id]
	if !ok {
		return ErrNotFound
	}
	current.Status = StatusFailed
	current.UpdatedAt = updatedAt.UTC()
	s.reads[id] = current
	return nil
}

// GetBlock implements Store.
func (s *MemoryStore) GetBlock(_ context.Context, userID, readID string) (DailyRead, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	read, ok := s.reads[readID]
	if !ok || read.UserID != userID {
		return DailyRead{}, ErrNotFound
	}
	return cloneRead(read), nil
}

// GetLatestReadyBefore implements Store.
func (s *MemoryStore) GetLatestReadyBefore(_ context.Context, userID string, beforeDate time.Time) (DailyRead, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	before := normalizeDateUTC(beforeDate)
	var best *DailyRead
	for _, read := range s.reads {
		if read.UserID != userID || read.Status != StatusReady {
			continue
		}
		date := normalizeDateUTC(read.GenDate)
		if !date.Before(before) {
			continue
		}
		if best == nil || date.After(normalizeDateUTC(best.GenDate)) {
			copy := read
			best = &copy
		}
	}
	if best == nil {
		return DailyRead{}, ErrNotFound
	}
	return cloneRead(*best), nil
}

// ReassignUser implements Store.
func (s *MemoryStore) ReassignUser(_ context.Context, fromUserID, toUserID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, read := range s.reads {
		if read.UserID == fromUserID {
			read.UserID = toUserID
			s.reads[id] = read
		}
	}
	return nil
}

func userDateKey(userID string, genDate time.Time) string {
	date := normalizeDateUTC(genDate)
	return userID + "|" + date.Format("2006-01-02")
}

func cloneRead(read DailyRead) DailyRead {
	out := read
	out.AudioURL = cloneStringPtr(read.AudioURL)
	out.UsedBlockIDs = append(json.RawMessage(nil), read.UsedBlockIDs...)
	out.SourceRefs = append(json.RawMessage(nil), read.SourceRefs...)
	out.ReadScore = cloneFloatPtr(read.ReadScore)
	out.GenDate = normalizeDateUTC(read.GenDate)
	return out
}

func cloneStringPtr(value *string) *string {
	if value == nil {
		return nil
	}
	out := *value
	return &out
}

func cloneFloatPtr(value *float64) *float64 {
	if value == nil {
		return nil
	}
	out := *value
	return &out
}
