package materials

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"
)

// MemoryStore is the test/local materials store.
type MemoryStore struct {
	mu        sync.Mutex
	materials map[string]Material
}

// NewMemoryStore constructs an empty store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{materials: make(map[string]Material)}
}

// Ping always succeeds.
func (s *MemoryStore) Ping(context.Context) error { return nil }

// InsertMaterial inserts a queued material.
func (s *MemoryStore) InsertMaterial(_ context.Context, m Material) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.materials[m.ID]; ok {
		return fmt.Errorf("materials: duplicate id")
	}
	s.materials[m.ID] = cloneMaterial(m)
	return nil
}

// GetMaterial returns a material by id; does not hide other users (service does).
func (s *MemoryStore) GetMaterial(_ context.Context, _, materialID string) (Material, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.materials[materialID]
	if !ok {
		return Material{}, ErrNotFound
	}
	return cloneMaterial(m), nil
}

// MarkProcessing moves queued → processing, and starts the reclaim lease.
func (s *MemoryStore) MarkProcessing(_ context.Context, materialID string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.materials[materialID]
	if !ok {
		return ErrNotFound
	}
	if m.RefineStatus != StatusQueued {
		return ErrConflict
	}
	ts := at.UTC()
	m.RefineStatus = StatusProcessing
	m.attempts++
	m.lockedAt = &ts
	m.UpdatedAt = ts
	s.materials[materialID] = m
	return nil
}

// MarkRefined moves processing → ready.
func (s *MemoryStore) MarkRefined(_ context.Context, materialID string, blockCount int, errorCode string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.materials[materialID]
	if !ok {
		return ErrNotFound
	}
	if m.RefineStatus != StatusProcessing {
		return ErrConflict
	}
	m.RefineStatus = StatusReady
	m.BlockCount = blockCount
	m.ErrorCode = errorCode
	m.lockedAt = nil
	m.UpdatedAt = at
	s.materials[materialID] = m
	return nil
}

// MarkRefineFailed moves processing → failed.
func (s *MemoryStore) MarkRefineFailed(_ context.Context, materialID, errorCode string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	m, ok := s.materials[materialID]
	if !ok {
		return ErrNotFound
	}
	if m.RefineStatus != StatusProcessing {
		return ErrConflict
	}
	m.RefineStatus = StatusFailed
	m.ErrorCode = errorCode
	m.lockedAt = nil
	m.UpdatedAt = at
	s.materials[materialID] = m
	return nil
}

// ReclaimExpired requeues processing rows whose lease expired, and fails the
// ones that already used up their attempts.
func (s *MemoryStore) ReclaimExpired(_ context.Context, at time.Time) (ReclaimResult, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := at.UTC()
	cutoff := ts.Add(-DefaultRefineLease)
	var out ReclaimResult
	for id, m := range s.materials {
		if m.RefineStatus != StatusProcessing || m.lockedAt == nil || m.DeletedAt != nil {
			continue
		}
		if m.lockedAt.After(cutoff) {
			continue
		}
		if m.attempts >= MaxRefineAttempts {
			m.RefineStatus = StatusFailed
			m.ErrorCode = ErrorLeaseExpired
			out.Expired++
		} else {
			m.RefineStatus = StatusQueued
			out.Requeued = append(out.Requeued, id)
		}
		m.lockedAt = nil
		m.UpdatedAt = ts
		s.materials[id] = m
	}
	sort.Strings(out.Requeued)
	return out, nil
}

// SoftDeleteAllForUser sets deleted_at.
func (s *MemoryStore) SoftDeleteAllForUser(_ context.Context, userID string, at time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	ts := at.UTC()
	for id, m := range s.materials {
		if m.UserID != userID || m.DeletedAt != nil {
			continue
		}
		m.DeletedAt = &ts
		m.UpdatedAt = ts
		s.materials[id] = m
		n++
	}
	return n, nil
}

// RestoreDeletedForUser clears deleted_at.
func (s *MemoryStore) RestoreDeletedForUser(_ context.Context, userID string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for id, m := range s.materials {
		if m.UserID != userID || m.DeletedAt == nil {
			continue
		}
		m.DeletedAt = nil
		s.materials[id] = m
		n++
	}
	return n, nil
}

func cloneMaterial(m Material) Material {
	out := m
	if m.DeletedAt != nil {
		t := *m.DeletedAt
		out.DeletedAt = &t
	}
	if m.lockedAt != nil {
		t := *m.lockedAt
		out.lockedAt = &t
	}
	return out
}
