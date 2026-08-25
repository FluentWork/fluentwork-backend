package session

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// MemoryStore is a process-local Store used by tests and local development.
type MemoryStore struct {
	mu       sync.Mutex
	sessions map[string]Session
	tickets  map[string]Ticket
}

// NewMemoryStore constructs an empty in-memory session store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		sessions: make(map[string]Session),
		tickets:  make(map[string]Ticket),
	}
}

// Ping always succeeds.
func (s *MemoryStore) Ping(context.Context) error {
	return nil
}

// CreateSession inserts a practice session.
func (s *MemoryStore) CreateSession(_ context.Context, session Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.sessions[session.ID]; exists {
		return fmt.Errorf("session: duplicate session id")
	}
	s.sessions[session.ID] = cloneSession(session)
	return nil
}

// GetSession returns a session by id.
func (s *MemoryStore) GetSession(_ context.Context, id string) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[id]
	if !ok {
		return Session{}, ErrNotFound
	}
	return cloneSession(session), nil
}

// CreateTicket inserts a one-time WSS ticket.
func (s *MemoryStore) CreateTicket(_ context.Context, ticket Ticket) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.tickets[ticket.Hash]; exists {
		return fmt.Errorf("session: duplicate ticket hash")
	}
	s.tickets[ticket.Hash] = cloneTicket(ticket)
	return nil
}

// GetTicketByHash returns a ticket by its hashed raw value.
func (s *MemoryStore) GetTicketByHash(_ context.Context, hash string) (Ticket, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ticket, ok := s.tickets[hash]
	if !ok {
		return Ticket{}, ErrNotFound
	}
	return cloneTicket(ticket), nil
}

// ReassignUser moves all sessions from a guest onto a registered account.
func (s *MemoryStore) ReassignUser(_ context.Context, fromUserID, toUserID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	for id, session := range s.sessions {
		if session.UserID == fromUserID {
			session.UserID = toUserID
			session.UpdatedAt = now
			s.sessions[id] = session
		}
	}
	for hash, ticket := range s.tickets {
		if ticket.UserID == fromUserID {
			ticket.UserID = toUserID
			s.tickets[hash] = ticket
		}
	}
	return nil
}

func cloneSession(session Session) Session {
	cloned := session
	if session.MaterialID != nil {
		copied := *session.MaterialID
		cloned.MaterialID = &copied
	}
	return cloned
}

func cloneTicket(ticket Ticket) Ticket {
	cloned := ticket
	if ticket.UsedAt != nil {
		copied := *ticket.UsedAt
		cloned.UsedAt = &copied
	}
	return cloned
}
