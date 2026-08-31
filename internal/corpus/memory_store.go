package corpus

import (
	"context"
	"sort"
	"strings"
	"sync"
	"time"
)

// MemoryStore keeps phrase blocks in memory for local development and tests.
type MemoryStore struct {
	mu     sync.Mutex
	blocks map[string]PhraseBlock
}

// NewMemoryStore constructs an in-memory corpus store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		blocks: make(map[string]PhraseBlock),
	}
}

// Ping implements Store.
func (s *MemoryStore) Ping(context.Context) error {
	return nil
}

// ListBlocks implements Store.
func (s *MemoryStore) ListBlocks(_ context.Context, filter ListFilter) ([]PhraseBlock, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	items := make([]PhraseBlock, 0, len(s.blocks))
	for _, block := range s.blocks {
		if block.UserID != filter.UserID || block.DeletedAt != nil {
			continue
		}
		if filter.SceneTag != "" && block.SceneTag != filter.SceneTag {
			continue
		}
		if filter.FunctionTag != "" && block.FunctionTag != filter.FunctionTag {
			continue
		}
		if filter.FavoriteOnly && !block.IsFavorite {
			continue
		}
		if kw := strings.ToLower(strings.TrimSpace(filter.Keyword)); kw != "" {
			haystack := strings.ToLower(block.ExpressionEN + " " + block.IntentZH)
			if !strings.Contains(haystack, kw) {
				continue
			}
		}
		if filter.After != nil && !isAfterCursor(block, *filter.After) {
			continue
		}
		items = append(items, cloneBlock(block))
	}
	sort.Slice(items, func(i, j int) bool {
		return compareBlocks(items[i], items[j]) < 0
	})
	if len(items) > filter.Limit {
		items = items[:filter.Limit]
	}
	return items, nil
}

// GetBlock implements Store.
func (s *MemoryStore) GetBlock(_ context.Context, userID, blockID string) (PhraseBlock, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	block, ok := s.blocks[blockID]
	if !ok || block.UserID != userID || block.DeletedAt != nil {
		return PhraseBlock{}, ErrNotFound
	}
	return cloneBlock(block), nil
}

// SaveAcceptedBlocks implements Store.
func (s *MemoryStore) SaveAcceptedBlocks(_ context.Context, blocks []PhraseBlock) ([]PhraseBlock, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	saved := make([]PhraseBlock, 0, len(blocks))
	for _, block := range blocks {
		existingID := ""
		for id, current := range s.blocks {
			if sameNaturalKey(current, block) {
				existingID = id
				break
			}
		}
		if existingID != "" {
			current := s.blocks[existingID]
			if current.DeletedAt != nil {
				current.DeletedAt = nil
				current.IntentZH = block.IntentZH
				current.ExpressionEN = block.ExpressionEN
				current.AnchorUserSaid = block.AnchorUserSaid
				current.SceneTag = block.SceneTag
				current.FunctionTag = block.FunctionTag
				current.SourceSessionID = block.SourceSessionID
				current.UpdatedAt = block.UpdatedAt
				s.blocks[existingID] = current
			}
			saved = append(saved, cloneBlock(s.blocks[existingID]))
			continue
		}
		s.blocks[block.ID] = cloneBlock(block)
		saved = append(saved, cloneBlock(block))
	}
	sort.Slice(saved, func(i, j int) bool { return compareBlocks(saved[i], saved[j]) < 0 })
	return saved, nil
}

// UpdateBlock implements Store.
func (s *MemoryStore) UpdateBlock(_ context.Context, block PhraseBlock) (PhraseBlock, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	existing, ok := s.blocks[block.ID]
	if !ok || existing.UserID != block.UserID || existing.DeletedAt != nil {
		return PhraseBlock{}, ErrNotFound
	}
	block.CreatedAt = existing.CreatedAt
	block.State = existing.State
	block.SuccessStreak = existing.SuccessStreak
	block.NextDueAt = existing.NextDueAt
	block.EaseFactor = existing.EaseFactor
	block.RealUseCount = existing.RealUseCount
	block.IsFavorite = existing.IsFavorite
	block.PinnedAt = cloneTimePtr(existing.PinnedAt)
	block.SourceSessionID = cloneStringPtr(existing.SourceSessionID)
	block.DeletedAt = nil
	s.blocks[block.ID] = cloneBlock(block)
	return cloneBlock(block), nil
}

// SetFavorite implements Store.
func (s *MemoryStore) SetFavorite(_ context.Context, userID, blockID string, isFavorite bool, pinnedAt *time.Time, updatedAt time.Time) (PhraseBlock, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	block, ok := s.blocks[blockID]
	if !ok || block.UserID != userID || block.DeletedAt != nil {
		return PhraseBlock{}, ErrNotFound
	}
	block.IsFavorite = isFavorite
	block.PinnedAt = cloneTimePtr(pinnedAt)
	block.UpdatedAt = updatedAt
	s.blocks[blockID] = block
	return cloneBlock(block), nil
}

// SoftDeleteBlock implements Store.
func (s *MemoryStore) SoftDeleteBlock(_ context.Context, userID, blockID string, deletedAt time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	block, ok := s.blocks[blockID]
	if !ok || block.UserID != userID || block.DeletedAt != nil {
		return ErrNotFound
	}
	block.DeletedAt = &deletedAt
	block.UpdatedAt = deletedAt
	s.blocks[blockID] = block
	return nil
}

// ReassignUser implements Store.
func (s *MemoryStore) ReassignUser(_ context.Context, fromUserID, toUserID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for id, block := range s.blocks {
		if block.UserID != fromUserID {
			continue
		}
		block.UserID = toUserID
		s.blocks[id] = block
	}
	return nil
}

func sameNaturalKey(left, right PhraseBlock) bool {
	return left.UserID == right.UserID &&
		derefString(left.SourceSessionID) == derefString(right.SourceSessionID) &&
		left.ExpressionEN == right.ExpressionEN &&
		left.AnchorUserSaid == right.AnchorUserSaid &&
		left.SceneTag == right.SceneTag &&
		left.FunctionTag == right.FunctionTag
}

func compareBlocks(left, right PhraseBlock) int {
	lp := pinnedValue(left.PinnedAt)
	rp := pinnedValue(right.PinnedAt)
	switch {
	case lp.After(rp):
		return -1
	case lp.Before(rp):
		return 1
	case left.CreatedAt.After(right.CreatedAt):
		return -1
	case left.CreatedAt.Before(right.CreatedAt):
		return 1
	case left.ID > right.ID:
		return -1
	case left.ID < right.ID:
		return 1
	default:
		return 0
	}
}

func isAfterCursor(block PhraseBlock, cursor ListCursor) bool {
	bp := pinnedValue(block.PinnedAt)
	cp := pinnedValue(cursor.PinnedAt)
	switch {
	case bp.Before(cp):
		return true
	case bp.After(cp):
		return false
	case block.CreatedAt.Before(cursor.CreatedAt):
		return true
	case block.CreatedAt.After(cursor.CreatedAt):
		return false
	default:
		return block.ID < cursor.ID
	}
}

func pinnedValue(value *time.Time) time.Time {
	if value == nil {
		return time.Time{}
	}
	return value.UTC()
}

func cloneBlock(block PhraseBlock) PhraseBlock {
	out := block
	out.PinnedAt = cloneTimePtr(block.PinnedAt)
	out.SourceSessionID = cloneStringPtr(block.SourceSessionID)
	out.DeletedAt = cloneTimePtr(block.DeletedAt)
	return out
}

func cloneTimePtr(value *time.Time) *time.Time {
	if value == nil {
		return nil
	}
	out := *value
	return &out
}

func cloneStringPtr(value *string) *string {
	if value == nil {
		return nil
	}
	out := *value
	return &out
}

func derefString(value *string) string {
	if value == nil {
		return ""
	}
	return *value
}
