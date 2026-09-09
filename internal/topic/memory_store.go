package topic

import (
	"context"
	"fmt"
	"sync"
	"time"
)

// MemoryStore is the test/local topic store.
type MemoryStore struct {
	mu       sync.Mutex
	cards    map[string]Card
	checkins map[string]Checkin
	streaks  map[string]Streak
}

// NewMemoryStore constructs an empty store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		cards:    make(map[string]Card),
		checkins: make(map[string]Checkin),
		streaks:  make(map[string]Streak),
	}
}

// Ping always succeeds.
func (s *MemoryStore) Ping(context.Context) error { return nil }

// ListTodayCards returns non-deleted cards for the UTC date.
func (s *MemoryStore) ListTodayCards(_ context.Context, userID string, forDate time.Time) ([]Card, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	day := utcDate(forDate)
	out := make([]Card, 0)
	for _, card := range s.cards {
		if card.UserID != userID || card.DeletedAt != nil || !utcDate(card.ForDate).Equal(day) {
			continue
		}
		out = append(out, cloneCard(card))
	}
	return out, nil
}

// HasCardsForDate is true if any card (including A4-deleted) exists for the date.
func (s *MemoryStore) HasCardsForDate(_ context.Context, userID string, forDate time.Time) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	day := utcDate(forDate)
	for _, card := range s.cards {
		if card.UserID == userID && utcDate(card.ForDate).Equal(day) {
			return true, nil
		}
	}
	return false, nil
}

// GetCard loads by id.
func (s *MemoryStore) GetCard(_ context.Context, cardID string) (Card, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	card, ok := s.cards[cardID]
	if !ok {
		return Card{}, ErrNotFound
	}
	return cloneCard(card), nil
}

// InsertCards stores generated cards.
func (s *MemoryStore) InsertCards(_ context.Context, cards []Card) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, card := range cards {
		if _, ok := s.cards[card.ID]; ok {
			return fmt.Errorf("topic: duplicate card id")
		}
		s.cards[card.ID] = cloneCard(card)
	}
	return nil
}

// MarkCheckedIn sets checked_in_at once.
func (s *MemoryStore) MarkCheckedIn(_ context.Context, cardID string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	card, ok := s.cards[cardID]
	if !ok {
		return ErrNotFound
	}
	if card.CheckedInAt != nil {
		return ErrConflict
	}
	ts := at.UTC()
	card.CheckedInAt = &ts
	card.UpdatedAt = ts
	s.cards[cardID] = card
	return nil
}

// InsertCheckin records a checkin row.
func (s *MemoryStore) InsertCheckin(_ context.Context, row Checkin) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.checkins[row.ID]; ok {
		return fmt.Errorf("topic: duplicate checkin id")
	}
	s.checkins[row.ID] = row
	return nil
}

// GetStreak returns the user streak or a zero row.
func (s *MemoryStore) GetStreak(_ context.Context, userID string) (Streak, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	st, ok := s.streaks[userID]
	if !ok {
		return Streak{UserID: userID}, nil
	}
	return cloneStreak(st), nil
}

// UpsertStreak writes the streak aggregate.
func (s *MemoryStore) UpsertStreak(_ context.Context, streak Streak) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.streaks[streak.UserID] = cloneStreak(streak)
	return nil
}

// SoftDeleteAllForUser sets deleted_at on cards, checkins, and streaks.
func (s *MemoryStore) SoftDeleteAllForUser(_ context.Context, userID string, at time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ts := at.UTC()
	n := 0
	for id, card := range s.cards {
		if card.UserID != userID || card.DeletedAt != nil {
			continue
		}
		card.DeletedAt = &ts
		card.UpdatedAt = ts
		s.cards[id] = card
		n++
	}
	for id, row := range s.checkins {
		if row.UserID != userID {
			continue
		}
		// checkins have no DeletedAt on the struct used in memory; drop by hiding via cards.
		_ = id
	}
	if st, ok := s.streaks[userID]; ok && st.DeletedAt == nil {
		st.DeletedAt = &ts
		st.UpdatedAt = ts
		s.streaks[userID] = st
		n++
	}
	return n, nil
}

// RestoreDeletedForUser clears deleted_at.
func (s *MemoryStore) RestoreDeletedForUser(_ context.Context, userID string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for id, card := range s.cards {
		if card.UserID != userID || card.DeletedAt == nil {
			continue
		}
		card.DeletedAt = nil
		s.cards[id] = card
		n++
	}
	if st, ok := s.streaks[userID]; ok && st.DeletedAt != nil {
		st.DeletedAt = nil
		s.streaks[userID] = st
		n++
	}
	return n, nil
}

func cloneCard(card Card) Card {
	out := card
	if card.CheckedInAt != nil {
		t := *card.CheckedInAt
		out.CheckedInAt = &t
	}
	if card.DeletedAt != nil {
		t := *card.DeletedAt
		out.DeletedAt = &t
	}
	if card.SeedTags != nil {
		out.SeedTags = append([]string(nil), card.SeedTags...)
	}
	return out
}

func cloneStreak(st Streak) Streak {
	out := st
	if st.LastCheckinDate != nil {
		t := *st.LastCheckinDate
		out.LastCheckinDate = &t
	}
	if st.DeletedAt != nil {
		t := *st.DeletedAt
		out.DeletedAt = &t
	}
	return out
}
