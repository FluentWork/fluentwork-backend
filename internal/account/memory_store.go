package account

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// MemoryStore is a process-local Store used by tests and local development.
type MemoryStore struct {
	mu       sync.Mutex
	users    map[string]User
	byDevice map[string]string
	tokens   map[string]RefreshToken
}

// NewMemoryStore constructs an empty in-memory account store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		users:    make(map[string]User),
		byDevice: make(map[string]string),
		tokens:   make(map[string]RefreshToken),
	}
}

// Ping always succeeds.
func (s *MemoryStore) Ping(context.Context) error {
	return nil
}

// CreateUser inserts a user. Device IDs are unique among active holders.
func (s *MemoryStore) CreateUser(_ context.Context, user User) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.users[user.ID]; exists {
		return fmt.Errorf("account: duplicate user id")
	}
	if user.DeviceID != nil && *user.DeviceID != "" {
		if _, exists := s.byDevice[*user.DeviceID]; exists {
			return ErrDuplicateDeviceID
		}
		s.byDevice[*user.DeviceID] = user.ID
	}
	s.users[user.ID] = cloneUser(user)
	return nil
}

// GetUser returns a user by id.
func (s *MemoryStore) GetUser(_ context.Context, id string) (User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	user, ok := s.users[id]
	if !ok {
		return User{}, ErrNotFound
	}
	return cloneUser(user), nil
}

// GetActiveByDeviceID returns the active user currently bound to device_id.
func (s *MemoryStore) GetActiveByDeviceID(_ context.Context, deviceID string) (User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	id, ok := s.byDevice[deviceID]
	if !ok {
		return User{}, ErrNotFound
	}
	user, ok := s.users[id]
	if !ok || user.Status != UserStatusActive {
		return User{}, ErrNotFound
	}
	return cloneUser(user), nil
}

// MarkMerged transfers device_id onto the registered user and archives the guest.
func (s *MemoryStore) MarkMerged(_ context.Context, guestID, targetID, deviceID string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	guest, ok := s.users[guestID]
	if !ok || !guest.IsGuest || guest.Status != UserStatusActive {
		return ErrNotFound
	}
	target, ok := s.users[targetID]
	if !ok || target.Status != UserStatusActive {
		return ErrNotFound
	}

	if guest.DeviceID != nil {
		delete(s.byDevice, *guest.DeviceID)
	}
	if target.DeviceID != nil && *target.DeviceID != deviceID {
		delete(s.byDevice, *target.DeviceID)
	}
	guest.DeviceID = nil
	guest.Status = UserStatusMerged
	mergedInto := targetID
	guest.MergedIntoUserID = &mergedInto
	guest.UpdatedAt = at
	s.users[guestID] = guest

	target.DeviceID = cloneStringPtr(&deviceID)
	target.UpdatedAt = at
	s.users[targetID] = target
	s.byDevice[deviceID] = targetID
	return nil
}

// FindGuestMergedInto returns the most recently merged guest for a target user.
func (s *MemoryStore) FindGuestMergedInto(_ context.Context, targetID string) (User, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	var found User
	var ok bool
	for _, user := range s.users {
		if user.MergedIntoUserID != nil && *user.MergedIntoUserID == targetID && user.Status == UserStatusMerged {
			if !ok || user.UpdatedAt.After(found.UpdatedAt) {
				found = user
				ok = true
			}
		}
	}
	if !ok {
		return User{}, ErrNotFound
	}
	return cloneUser(found), nil
}

// ReplaceRefreshToken drops existing refresh tokens for the user and stores one.
func (s *MemoryStore) ReplaceRefreshToken(_ context.Context, token RefreshToken) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for hash, existing := range s.tokens {
		if existing.UserID == token.UserID {
			delete(s.tokens, hash)
		}
	}
	s.tokens[token.Hash] = token
	return nil
}

// DeleteRefreshTokensForUser removes refresh tokens for one user.
func (s *MemoryStore) DeleteRefreshTokensForUser(_ context.Context, userID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for hash, existing := range s.tokens {
		if existing.UserID == userID {
			delete(s.tokens, hash)
		}
	}
	return nil
}

func cloneUser(user User) User {
	cloned := user
	cloned.Email = cloneStringPtr(user.Email)
	cloned.Phone = cloneStringPtr(user.Phone)
	cloned.DeviceID = cloneStringPtr(user.DeviceID)
	cloned.PasswordHash = cloneStringPtr(user.PasswordHash)
	cloned.MergedIntoUserID = cloneStringPtr(user.MergedIntoUserID)
	return cloned
}

func cloneStringPtr(value *string) *string {
	if value == nil {
		return nil
	}
	copied := *value
	return &copied
}
