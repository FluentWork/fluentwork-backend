package corpus

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/FluentWork/fluentwork-backend/internal/apierr"
)

const (
	defaultListLimit = 20
	maxListLimit     = 100
	defaultEase      = 2.5
)

// Service implements corpus business rules over Store.
type Service struct {
	store  Store
	logger *slog.Logger
	now    func() time.Time
	newID  func() string
}

// NewService constructs the corpus service.
func NewService(store Store, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		store:  store,
		logger: logger.With("component", "corpus.service"),
		now:    time.Now,
		newID:  uuid.NewString,
	}
}

// Reassigner adapts Store to account guest merge.
type Reassigner struct {
	Store Store
}

// ReassignFromGuest moves guest-owned blocks to the registered user.
func (r Reassigner) ReassignFromGuest(ctx context.Context, guestUserID, targetUserID string) error {
	if r.Store == nil {
		return nil
	}
	return r.Store.ReassignUser(ctx, guestUserID, targetUserID)
}

// ListBlocks returns paginated phrase blocks for one user.
func (s *Service) ListBlocks(ctx context.Context, req ListBlocksRequest) (ListBlocksResponse, error) {
	userID := strings.TrimSpace(req.UserID)
	if userID == "" {
		return ListBlocksResponse{}, apierr.Unauthenticated("missing authenticated user")
	}
	var decodedCursor *ListCursor
	if strings.TrimSpace(req.Cursor) != "" {
		cursor, err := decodeCursor(req.Cursor)
		if err != nil {
			return ListBlocksResponse{}, apierr.InvalidArgument("cursor is invalid")
		}
		decodedCursor = &cursor
	}
	incremental := strings.TrimSpace(req.UpdatedAfter) != "" || (decodedCursor != nil && decodedCursor.Mode == CursorModeDelta)
	if incremental && (strings.TrimSpace(req.SceneTag) != "" || strings.TrimSpace(req.FunctionTag) != "" || strings.TrimSpace(req.Keyword) != "" || req.FavoriteOnly || req.PinnedOnly) {
		return ListBlocksResponse{}, apierr.InvalidArgument("updated_after cannot be combined with scene, func, kw, favorite_only, or pinned_only")
	}
	filter := ListFilter{
		UserID:       userID,
		SceneTag:     normalizeOptionalEnum(req.SceneTag),
		FunctionTag:  normalizeOptionalEnum(req.FunctionTag),
		Keyword:      strings.TrimSpace(req.Keyword),
		FavoriteOnly: req.FavoriteOnly,
		PinnedOnly:   req.PinnedOnly,
		Incremental:  incremental,
		Limit:        normalizeLimit(req.Limit),
	}
	if filter.SceneTag != "" {
		if _, ok := validSceneTags[filter.SceneTag]; !ok {
			return ListBlocksResponse{}, apierr.InvalidArgument("scene is invalid")
		}
	}
	if filter.FunctionTag != "" {
		if _, ok := validFunctionTags[filter.FunctionTag]; !ok {
			return ListBlocksResponse{}, apierr.InvalidArgument("func is invalid")
		}
	}
	if value := strings.TrimSpace(req.UpdatedAfter); value != "" {
		updatedAfter, err := time.Parse(time.RFC3339Nano, value)
		if err != nil {
			return ListBlocksResponse{}, apierr.InvalidArgument("updated_after is invalid")
		}
		updatedAfter = updatedAfter.UTC()
		filter.UpdatedAfter = &updatedAfter
	}
	if decodedCursor != nil {
		if incremental && decodedCursor.Mode == CursorModeBrowse {
			return ListBlocksResponse{}, apierr.InvalidArgument("cursor mode is invalid for updated_after")
		}
		if !incremental && decodedCursor.Mode == CursorModeDelta {
			return ListBlocksResponse{}, apierr.InvalidArgument("delta cursor requires updated_after")
		}
		filter.After = decodedCursor
	}

	blocks, err := s.store.ListBlocks(ctx, filter)
	if err != nil {
		return ListBlocksResponse{}, err
	}
	out := ListBlocksResponse{
		Items:       make([]PhraseBlockView, 0, len(blocks)),
		CursorReset: false,
	}
	for _, block := range blocks {
		out.Items = append(out.Items, toView(block))
	}
	if len(blocks) == filter.Limit {
		last := blocks[len(blocks)-1]
		cursor := ListCursor{ID: last.ID}
		if filter.Incremental {
			cursor.Mode = CursorModeDelta
			cursor.UpdatedAt = last.UpdatedAt
		} else {
			cursor.Mode = CursorModeBrowse
			cursor.PinnedAt = last.PinnedAt
			cursor.CreatedAt = last.CreatedAt
			cursor.UpdatedAt = last.UpdatedAt
			cursor.IsPinned = last.PinnedAt != nil
			cursor.IsFavorite = last.IsFavorite
		}
		out.NextCursor, err = encodeCursor(cursor)
		if err != nil {
			return ListBlocksResponse{}, err
		}
	}
	return out, nil
}

// UpdateBlock edits one owned phrase block.
func (s *Service) UpdateBlock(ctx context.Context, userID, blockID string, req UpdateBlockRequest) (PhraseBlockView, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return PhraseBlockView{}, apierr.Unauthenticated("missing authenticated user")
	}
	block, err := s.store.GetBlock(ctx, userID, strings.TrimSpace(blockID))
	if err != nil {
		if err == ErrNotFound {
			return PhraseBlockView{}, apierr.NotFound("block not found")
		}
		return PhraseBlockView{}, err
	}
	if err := applyEditableFields(&block, req); err != nil {
		return PhraseBlockView{}, err
	}
	block.UpdatedAt = s.now().UTC()
	saved, err := s.store.UpdateBlock(ctx, block)
	if err != nil {
		if err == ErrNotFound {
			return PhraseBlockView{}, apierr.NotFound("block not found")
		}
		return PhraseBlockView{}, err
	}
	return toView(saved), nil
}

// SetFavorite toggles favorite/pinned state for one block.
func (s *Service) SetFavorite(ctx context.Context, userID, blockID string, req FavoriteBlockRequest) (PhraseBlockView, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return PhraseBlockView{}, apierr.Unauthenticated("missing authenticated user")
	}
	var pinnedAt *time.Time
	now := s.now().UTC()
	if req.Pinned {
		pinnedAt = &now
	}
	if !req.IsFavorite {
		pinnedAt = nil
	}
	saved, err := s.store.SetFavorite(ctx, userID, strings.TrimSpace(blockID), req.IsFavorite, pinnedAt, now)
	if err != nil {
		if err == ErrNotFound {
			return PhraseBlockView{}, apierr.NotFound("block not found")
		}
		return PhraseBlockView{}, err
	}
	return toView(saved), nil
}

func (s *Service) authorizeBlock(ctx context.Context, userID, blockID string) (PhraseBlock, error) {
	block, err := s.store.PeekBlock(ctx, strings.TrimSpace(blockID))
	if err != nil {
		if err == ErrNotFound {
			return PhraseBlock{}, apierr.NotFound("block not found")
		}
		return PhraseBlock{}, err
	}
	if block.DeletedAt != nil {
		return PhraseBlock{}, apierr.NotFound("block not found")
	}
	if block.UserID != userID {
		return PhraseBlock{}, apierr.PermissionDenied("block belongs to another user")
	}
	return block, nil
}

// UpdatePin sets or clears pinned_at without changing is_favorite.
func (s *Service) UpdatePin(ctx context.Context, userID, blockID string, pinned bool) (PhraseBlockView, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return PhraseBlockView{}, apierr.Unauthenticated("missing authenticated user")
	}
	block, err := s.authorizeBlock(ctx, userID, blockID)
	if err != nil {
		return PhraseBlockView{}, err
	}
	now := s.now().UTC()
	var pinnedAt *time.Time
	if pinned {
		pinnedAt = &now
	}
	saved, err := s.store.SetFavorite(ctx, userID, block.ID, block.IsFavorite, pinnedAt, now)
	if err != nil {
		if err == ErrNotFound {
			return PhraseBlockView{}, apierr.NotFound("block not found")
		}
		return PhraseBlockView{}, err
	}
	return toView(saved), nil
}

// UpdateFavorite sets is_favorite without changing pinned_at.
func (s *Service) UpdateFavorite(ctx context.Context, userID, blockID string, favorite bool) (PhraseBlockView, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return PhraseBlockView{}, apierr.Unauthenticated("missing authenticated user")
	}
	block, err := s.authorizeBlock(ctx, userID, blockID)
	if err != nil {
		return PhraseBlockView{}, err
	}
	now := s.now().UTC()
	saved, err := s.store.SetFavorite(ctx, userID, block.ID, favorite, block.PinnedAt, now)
	if err != nil {
		if err == ErrNotFound {
			return PhraseBlockView{}, apierr.NotFound("block not found")
		}
		return PhraseBlockView{}, err
	}
	return toView(saved), nil
}

// DeleteBlock soft-deletes one owned phrase block.
func (s *Service) DeleteBlock(ctx context.Context, userID, blockID string) error {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return apierr.Unauthenticated("missing authenticated user")
	}
	if err := s.store.SoftDeleteBlock(ctx, userID, strings.TrimSpace(blockID), s.now().UTC()); err != nil {
		if err == ErrNotFound {
			return apierr.NotFound("block not found")
		}
		return err
	}
	return nil
}

// BatchAccept idempotently stores refine blocks from one session.
func (s *Service) BatchAccept(ctx context.Context, userID string, req BatchAcceptRequest) (BatchAcceptResponse, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return BatchAcceptResponse{}, apierr.Unauthenticated("missing authenticated user")
	}
	sourceSessionID := strings.TrimSpace(req.SourceSessionID)
	if sourceSessionID == "" {
		return BatchAcceptResponse{}, apierr.InvalidArgument("source_session_id is required")
	}
	if len(req.Blocks) == 0 {
		return BatchAcceptResponse{}, apierr.InvalidArgument("blocks is required")
	}
	now := s.now().UTC()
	blocks := make([]PhraseBlock, 0, len(req.Blocks))
	for _, item := range req.Blocks {
		block, err := newAcceptedBlock(userID, sourceSessionID, now, s.newID(), item)
		if err != nil {
			return BatchAcceptResponse{}, err
		}
		blocks = append(blocks, block)
	}
	saved, err := s.store.SaveAcceptedBlocks(ctx, blocks)
	if err != nil {
		return BatchAcceptResponse{}, err
	}
	resp := BatchAcceptResponse{
		AcceptedCount: len(saved),
		Items:         make([]PhraseBlockView, 0, len(saved)),
	}
	for _, block := range saved {
		resp.Items = append(resp.Items, toView(block))
	}
	return resp, nil
}

func toView(block PhraseBlock) PhraseBlockView {
	return PhraseBlockView{
		ID:              block.ID,
		IntentZH:        block.IntentZH,
		ExpressionEN:    block.ExpressionEN,
		AnchorUserSaid:  block.AnchorUserSaid,
		SceneTag:        block.SceneTag,
		FunctionTag:     block.FunctionTag,
		State:           block.State,
		SuccessStreak:   block.SuccessStreak,
		NextDueAt:       block.NextDueAt,
		EaseFactor:      block.EaseFactor,
		RealUseCount:    block.RealUseCount,
		IsFavorite:      block.IsFavorite,
		PinnedAt:        block.PinnedAt,
		SourceSessionID: block.SourceSessionID,
		DeletedAt:       block.DeletedAt,
		CreatedAt:       block.CreatedAt,
		UpdatedAt:       block.UpdatedAt,
	}
}

func newAcceptedBlock(userID, sourceSessionID string, now time.Time, id string, item BatchAcceptBlock) (PhraseBlock, error) {
	block := PhraseBlock{
		ID:              id,
		UserID:          userID,
		IntentZH:        strings.TrimSpace(item.IntentZH),
		ExpressionEN:    strings.TrimSpace(item.ExpressionEN),
		AnchorUserSaid:  strings.TrimSpace(item.AnchorUserSaid),
		SceneTag:        normalizeOptionalEnum(item.SceneTag),
		FunctionTag:     normalizeOptionalEnum(item.FunctionTag),
		State:           StateNew,
		SuccessStreak:   0,
		NextDueAt:       nextDueForNew(now),
		EaseFactor:      defaultEase,
		RealUseCount:    0,
		SourceSessionID: stringPtr(sourceSessionID),
		CreatedAt:       now,
		UpdatedAt:       now,
	}
	if err := validateAcceptedBlock(block); err != nil {
		return PhraseBlock{}, err
	}
	return block, nil
}

func applyEditableFields(block *PhraseBlock, req UpdateBlockRequest) error {
	block.IntentZH = strings.TrimSpace(req.IntentZH)
	block.ExpressionEN = strings.TrimSpace(req.ExpressionEN)
	block.AnchorUserSaid = strings.TrimSpace(req.AnchorUserSaid)
	block.SceneTag = normalizeOptionalEnum(req.SceneTag)
	block.FunctionTag = normalizeOptionalEnum(req.FunctionTag)
	return validateAcceptedBlock(*block)
}

func validateAcceptedBlock(block PhraseBlock) error {
	switch {
	case strings.TrimSpace(block.IntentZH) == "":
		return apierr.InvalidArgument("intent_zh is required")
	case strings.TrimSpace(block.ExpressionEN) == "":
		return apierr.InvalidArgument("expression_en is required")
	case strings.TrimSpace(block.AnchorUserSaid) == "":
		return apierr.InvalidArgument("anchor_user_said is required")
	}
	if _, ok := validSceneTags[block.SceneTag]; !ok {
		return apierr.InvalidArgument("scene_tag is invalid")
	}
	if _, ok := validFunctionTags[block.FunctionTag]; !ok {
		return apierr.InvalidArgument("function_tag is invalid")
	}
	if _, ok := validStates[block.State]; !ok {
		return apierr.InvalidArgument("state is invalid")
	}
	return nil
}

func normalizeLimit(limit int) int {
	switch {
	case limit <= 0:
		return defaultListLimit
	case limit > maxListLimit:
		return maxListLimit
	default:
		return limit
	}
}

func normalizeOptionalEnum(value string) string {
	return strings.ToLower(strings.TrimSpace(value))
}

func nextDueForNew(now time.Time) time.Time {
	base := now.UTC().Truncate(24 * time.Hour)
	return base.Add(24 * time.Hour)
}

func stringPtr(value string) *string {
	if strings.TrimSpace(value) == "" {
		return nil
	}
	out := value
	return &out
}

func encodeCursor(cursor ListCursor) (string, error) {
	payload := map[string]string{
		"id": cursor.ID,
	}
	if cursor.Mode == "" {
		cursor.Mode = CursorModeBrowse
	}
	payload["mode"] = string(cursor.Mode)
	switch cursor.Mode {
	case CursorModeDelta:
		payload["updated_at"] = cursor.UpdatedAt.UTC().Format(time.RFC3339Nano)
	default:
		createdAt := cursor.CreatedAt
		if createdAt.IsZero() {
			createdAt = cursor.UpdatedAt
		}
		payload["created_at"] = createdAt.UTC().Format(time.RFC3339Nano)
		payload["updated_at"] = cursor.UpdatedAt.UTC().Format(time.RFC3339Nano)
		payload["is_pinned"] = boolCursorFlag(cursor.IsPinned)
		payload["is_favorite"] = boolCursorFlag(cursor.IsFavorite)
		if cursor.PinnedAt != nil {
			payload["pinned_at"] = cursor.PinnedAt.UTC().Format(time.RFC3339Nano)
		}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return "", fmt.Errorf("marshal cursor: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(raw), nil
}

func decodeCursor(token string) (ListCursor, error) {
	raw, err := base64.RawURLEncoding.DecodeString(token)
	if err != nil {
		return ListCursor{}, err
	}
	var payload map[string]string
	if err := json.Unmarshal(raw, &payload); err != nil {
		return ListCursor{}, err
	}
	cursor := ListCursor{
		ID:   strings.TrimSpace(payload["id"]),
		Mode: CursorMode(strings.TrimSpace(payload["mode"])),
	}
	if cursor.ID == "" {
		return ListCursor{}, fmt.Errorf("missing id")
	}
	if cursor.Mode == "" {
		if strings.TrimSpace(payload["updated_at"]) != "" {
			cursor.Mode = CursorModeDelta
		} else {
			cursor.Mode = CursorModeBrowse
		}
	}
	switch cursor.Mode {
	case CursorModeDelta:
		updatedAt, err := time.Parse(time.RFC3339Nano, payload["updated_at"])
		if err != nil {
			return ListCursor{}, err
		}
		cursor.UpdatedAt = updatedAt.UTC()
	case CursorModeBrowse:
		if value := strings.TrimSpace(payload["created_at"]); value != "" {
			createdAt, err := time.Parse(time.RFC3339Nano, value)
			if err != nil {
				return ListCursor{}, err
			}
			cursor.CreatedAt = createdAt.UTC()
		}
		if value := strings.TrimSpace(payload["updated_at"]); value != "" {
			updatedAt, err := time.Parse(time.RFC3339Nano, value)
			if err != nil {
				return ListCursor{}, err
			}
			cursor.UpdatedAt = updatedAt.UTC()
		} else {
			cursor.UpdatedAt = cursor.CreatedAt
		}
		if value := strings.TrimSpace(payload["pinned_at"]); value != "" {
			pinnedAt, err := time.Parse(time.RFC3339Nano, value)
			if err != nil {
				return ListCursor{}, err
			}
			pinnedAt = pinnedAt.UTC()
			cursor.PinnedAt = &pinnedAt
			cursor.IsPinned = true
		}
		if value := strings.TrimSpace(payload["is_pinned"]); value != "" {
			cursor.IsPinned = value == "1" || value == "true"
		}
		if value := strings.TrimSpace(payload["is_favorite"]); value != "" {
			cursor.IsFavorite = value == "1" || value == "true"
		}
	default:
		return ListCursor{}, fmt.Errorf("invalid cursor mode")
	}
	return cursor, nil
}

func boolCursorFlag(value bool) string {
	if value {
		return "1"
	}
	return "0"
}
