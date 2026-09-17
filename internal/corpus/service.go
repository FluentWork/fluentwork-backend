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
	previousExpression := block.ExpressionEN
	if err := applyEditableFields(&block, req); err != nil {
		return PhraseBlockView{}, err
	}
	now := s.now().UTC()
	block.UpdatedAt = now
	saved, err := s.store.UpdateBlock(ctx, block)
	if err != nil {
		if err == ErrNotFound {
			return PhraseBlockView{}, apierr.NotFound("block not found")
		}
		return PhraseBlockView{}, err
	}
	if saved.ExpressionEN == previousExpression {
		// Intent, anchor and tags refine how a phrase is described, not which
		// phrase is being recalled — no reset, no new version.
		return toView(saved), nil
	}

	// The sentence changed, so this is a different thing to recall (86_ M6).
	// The edit is recorded for auditability and the schedule goes back to 灰: a
	// green light earned on the old wording would claim fluency in a sentence
	// the learner has never said.
	edit := BlockEdit{
		ID: s.newID(), UserID: userID, BlockID: saved.ID,
		Version:       saved.ExpressionVersion + 1,
		OldExpression: previousExpression,
		NewExpression: saved.ExpressionEN,
		CreatedAt:     now,
	}
	if err := s.store.SaveBlockEdit(ctx, edit); err != nil {
		// The text change itself succeeded; losing the audit row is a
		// bookkeeping problem, and the version staying put is visible evidence
		// of it.
		s.logger.Warn("block edit not recorded", "block_id", saved.ID, "user_id", userID, "err", err)
	}
	reset, err := s.store.ResetSchedule(ctx, userID, saved.ID, now)
	if err != nil {
		s.logger.Warn("block schedule not reset after an edit", "block_id", saved.ID, "err", err)
		return toView(saved), nil
	}
	return toView(reset), nil
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
// dedupeScanLimit bounds the corpus scan one accept performs. Comparison is
// in-memory on purpose: a normalised-expression column would need a backfill
// whose SQL normalisation could drift from the Go one, and a user's corpus is
// bounded by this scan anyway.
const dedupeScanLimit = 500

// NormalizeExpression is the identity a phrase block is deduplicated by:
// case, surrounding punctuation and inner spacing folded away, so the same
// sentence refined from two sessions is one row (86_ M5).
func NormalizeExpression(expression string) string {
	fields := strings.FieldsFunc(strings.ToLower(strings.TrimSpace(expression)), func(r rune) bool {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r >= 0x4e00 && r <= 0x9fff:
			return false
		default:
			return true
		}
	})
	return strings.Join(fields, " ")
}

// realUseMaxBlocks bounds one credit request.
const realUseMaxBlocks = 50

// RecordRealUse credits a learner's confirmed use of their own blocks.
//
// It exists because a checkin is evidence the server cannot observe on its own
// (86_ M10): a B7 hit is what we detected, a checkin is what the learner says
// happened with a real person. Both credit real_use_count and count as one
// successful recall (PRD §5.2.3), but they are recorded under different sources
// so the two can be told apart later.
func (s *Service) RecordRealUse(ctx context.Context, userID string, blockIDs []string, source, refID string) (int, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return 0, apierr.Unauthenticated("missing authenticated user")
	}
	switch source {
	case RealUseSourceHit, RealUseSourceCheckin:
	default:
		return 0, apierr.InvalidArgument("source must be hit or checkin")
	}
	refID = strings.TrimSpace(refID)
	if refID == "" {
		return 0, apierr.InvalidArgument("ref_id is required")
	}
	cleaned := make([]string, 0, len(blockIDs))
	for _, id := range blockIDs {
		if id = strings.TrimSpace(id); id != "" {
			cleaned = append(cleaned, id)
		}
	}
	if len(cleaned) == 0 {
		return 0, nil
	}
	if len(cleaned) > realUseMaxBlocks {
		return 0, apierr.InvalidArgument("too many blocks in one credit request")
	}
	if s == nil || s.store == nil {
		return 0, apierr.Internal("corpus store is not configured")
	}
	return s.store.RecordRealUses(ctx, userID, cleaned, source, refID, s.now().UTC())
}

// CountRealUsesBySource returns the L1/L2 split of credited uses since a time.
func (s *Service) CountRealUsesBySource(ctx context.Context, userID string, since time.Time) (map[string]int, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, apierr.Unauthenticated("missing authenticated user")
	}
	if s == nil || s.store == nil {
		return nil, apierr.Internal("corpus store is not configured")
	}
	return s.store.CountRealUsesBySource(ctx, userID, since)
}

// BatchAccept admits refine cards into the corpus.
//
// A phrase the learner already owns is not admitted again: the existing block is
// returned instead, because two near-identical rows are two weaker assets, not
// two assets (86_ M5: 15% of refined expressions repeated across sessions).
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

	// What the learner already owns, by normalised expression: a phrase refined
	// from a later session joins the block that phrase already is, instead of
	// becoming a near-duplicate that dilutes the corpus (86_ M5).
	existing, err := s.store.ListBlocks(ctx, ListFilter{UserID: userID, Limit: dedupeScanLimit})
	if err != nil {
		return BatchAcceptResponse{}, err
	}
	byExpression := make(map[string]PhraseBlock, len(existing))
	for _, block := range existing {
		if block.DeletedAt != nil {
			continue
		}
		byExpression[NormalizeExpression(block.ExpressionEN)] = block
	}

	blocks := make([]PhraseBlock, 0, len(req.Blocks))
	merged := make([]PhraseBlock, 0, len(req.Blocks))
	for _, item := range req.Blocks {
		block, err := newAcceptedBlock(userID, sourceSessionID, now, s.newID(), item)
		if err != nil {
			return BatchAcceptResponse{}, err
		}
		if prev, ok := byExpression[NormalizeExpression(block.ExpressionEN)]; ok {
			merged = append(merged, prev)
			continue
		}
		// Two identical expressions inside one request collapse too.
		byExpression[NormalizeExpression(block.ExpressionEN)] = block
		blocks = append(blocks, block)
	}

	saved, err := s.store.SaveAcceptedBlocks(ctx, blocks)
	if err != nil {
		return BatchAcceptResponse{}, err
	}
	// Merged blocks come back as items as well: the client still wants to reach
	// them, and MergedCount tells it how many were already there.
	resp := BatchAcceptResponse{
		AcceptedCount: len(saved),
		MergedCount:   len(merged),
		Items:         make([]PhraseBlockView, 0, len(saved)+len(merged)),
	}
	for _, block := range saved {
		resp.Items = append(resp.Items, toView(block))
	}
	for _, block := range merged {
		resp.Items = append(resp.Items, toView(block))
	}
	return resp, nil
}

// FeedbackRequest is POST /corpus/blocks/:id/feedback.
type FeedbackRequest struct {
	UserID  string
	BlockID string
	Reason  string
}

// FeedbackReasonCount is one bucket of the reflux summary.
type FeedbackReasonCount struct {
	Reason string `json:"reason"`
	Count  int    `json:"count"`
}

// FeedbackResponse reports what the tap did. Recorded is false when the same
// reason had already been reported for this block — the button is idempotent,
// not a counter of taps.
type FeedbackResponse struct {
	BlockID  string `json:"block_id"`
	Reason   string `json:"reason"`
	Recorded bool   `json:"recorded"`
}

// RecordFeedback stores one "this rewrite is not good enough" signal
// (83_ §2.2 风险 1). The signal is the point: it is what prompt iteration reads
// when deciding whether a rewrite rule actually landed.
func (s *Service) RecordFeedback(ctx context.Context, req FeedbackRequest) (FeedbackResponse, error) {
	userID := strings.TrimSpace(req.UserID)
	if userID == "" {
		return FeedbackResponse{}, apierr.Unauthenticated("missing authenticated user")
	}
	blockID := strings.TrimSpace(req.BlockID)
	if blockID == "" {
		return FeedbackResponse{}, apierr.InvalidArgument("block_id is required")
	}
	reason := strings.TrimSpace(req.Reason)
	if !ValidFeedbackReason(reason) {
		return FeedbackResponse{}, apierr.InvalidArgument("reason must be not_idiomatic, not_useful or wrong_meaning")
	}
	if s == nil || s.store == nil {
		return FeedbackResponse{}, apierr.Internal("corpus store is not configured")
	}
	block, err := s.store.PeekBlock(ctx, blockID)
	if err != nil {
		if err == ErrNotFound {
			return FeedbackResponse{}, apierr.NotFound("block not found")
		}
		return FeedbackResponse{}, err
	}
	// Someone else's block is NotFound, not Forbidden: saying it exists is the
	// leak (same rule as the judge and the appeal paths).
	if block.UserID != userID || block.DeletedAt != nil {
		return FeedbackResponse{}, apierr.NotFound("block not found")
	}
	first, err := s.store.SaveFeedback(ctx, Feedback{
		ID:        s.newID(),
		UserID:    userID,
		BlockID:   blockID,
		Reason:    reason,
		CreatedAt: s.now().UTC(),
	})
	if err != nil {
		return FeedbackResponse{}, err
	}
	if first {
		incFeedback(reason)
	}
	return FeedbackResponse{BlockID: blockID, Reason: reason, Recorded: first}, nil
}

// FeedbackSummary returns the user's live signals per reason.
func (s *Service) FeedbackSummary(ctx context.Context, userID string) ([]FeedbackReasonCount, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return nil, apierr.Unauthenticated("missing authenticated user")
	}
	if s == nil || s.store == nil {
		return nil, apierr.Internal("corpus store is not configured")
	}
	counts, err := s.store.CountFeedback(ctx, userID)
	if err != nil {
		return nil, err
	}
	out := make([]FeedbackReasonCount, 0, len(counts))
	for _, reason := range []string{FeedbackNotIdiomatic, FeedbackNotUseful, FeedbackWrongMeaning} {
		out = append(out, FeedbackReasonCount{Reason: reason, Count: counts[reason]})
	}
	return out, nil
}

func toView(block PhraseBlock) PhraseBlockView {
	return PhraseBlockView{
		ID:                block.ID,
		IntentZH:          block.IntentZH,
		ExpressionEN:      block.ExpressionEN,
		ExpressionVersion: block.ExpressionVersion,
		AnchorUserSaid:    block.AnchorUserSaid,
		SceneTag:          block.SceneTag,
		FunctionTag:       block.FunctionTag,
		State:             block.State,
		SuccessStreak:     block.SuccessStreak,
		NextDueAt:         block.NextDueAt,
		EaseFactor:        block.EaseFactor,
		RealUseCount:      block.RealUseCount,
		IsFavorite:        block.IsFavorite,
		PinnedAt:          block.PinnedAt,
		SourceSessionID:   block.SourceSessionID,
		DeletedAt:         block.DeletedAt,
		CreatedAt:         block.CreatedAt,
		UpdatedAt:         block.UpdatedAt,
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
