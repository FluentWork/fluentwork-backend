package topic

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/FluentWork/fluentwork-backend/internal/apierr"
	"github.com/FluentWork/fluentwork-backend/internal/corpus"
)

// BlockLookup resolves a card's 话术块清单 for display (PRD §7.8 H1). Optional:
// without it the cards still list block ids.
type BlockLookup interface {
	ListBlocks(ctx context.Context, filter corpus.ListFilter) ([]corpus.PhraseBlock, error)
}

// Service lists today's cards and records checkins.
type Service struct {
	store  Store
	gen    *Generator
	blocks BlockLookup
	logger *slog.Logger
	now    func() time.Time
	newID  func() string
}

// SetBlockLookup attaches the corpus reader used to resolve card block lists.
func (s *Service) SetBlockLookup(lookup BlockLookup) {
	if s == nil {
		return
	}
	s.blocks = lookup
}

// NewService constructs B23 topic service.
func NewService(store Store, gen *Generator, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		store:  store,
		gen:    gen,
		logger: logger.With("component", "topic"),
		now:    time.Now,
		newID:  uuid.NewString,
	}
}

// ListToday returns today's cards, generating on demand when the batch has not run.
func (s *Service) ListToday(ctx context.Context, userID string) (ListResponse, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return ListResponse{}, apierr.Unauthenticated("missing authenticated user")
	}
	today := utcDate(s.now())
	items, err := s.store.ListTodayCards(ctx, userID, today)
	if err != nil {
		return ListResponse{}, err
	}
	if len(items) == 0 && s.gen != nil {
		if genErr := s.gen.GenerateForUser(ctx, userID, today); genErr != nil && s.logger != nil {
			s.logger.Warn("on-demand topic generate", "user_id", userID, "err", genErr)
		}
		items, err = s.store.ListTodayCards(ctx, userID, today)
		if err != nil {
			return ListResponse{}, err
		}
	}
	if items == nil {
		items = []Card{}
	}
	return ListResponse{Items: s.viewCards(ctx, userID, items)}, nil
}

// viewCards attaches each card's block list, resolved from the learner's own
// corpus. A lookup failure costs the detail, not the cards.
func (s *Service) viewCards(ctx context.Context, userID string, cards []Card) []CardView {
	views := make([]CardView, 0, len(cards))
	if s.blocks == nil {
		for _, card := range cards {
			views = append(views, CardView{Card: card})
		}
		return views
	}
	byID := map[string]CardBlockRef{}
	blocks, err := s.blocks.ListBlocks(ctx, corpus.ListFilter{UserID: userID, Limit: 200})
	if err != nil {
		if s.logger != nil {
			s.logger.Warn("topic card block lookup failed", "user_id", userID, "err", err)
		}
	} else {
		for _, block := range blocks {
			byID[block.ID] = CardBlockRef{ID: block.ID, ExpressionEN: block.ExpressionEN, IntentZH: block.IntentZH}
		}
	}
	for _, card := range cards {
		view := CardView{Card: card}
		for _, id := range card.BlockIDs {
			if ref, ok := byID[id]; ok {
				view.Blocks = append(view.Blocks, ref)
			}
		}
		views = append(views, view)
	}
	return views
}

// Checkin records a card completion.
func (s *Service) Checkin(ctx context.Context, userID, cardID, reflection string) (CheckinResult, error) {
	userID = strings.TrimSpace(userID)
	cardID = strings.TrimSpace(cardID)
	if userID == "" {
		return CheckinResult{}, apierr.Unauthenticated("missing authenticated user")
	}
	if cardID == "" {
		return CheckinResult{}, apierr.InvalidArgument("card_id is required")
	}
	if len(reflection) > MaxReflectionLen {
		return CheckinResult{}, apierr.InvalidArgument("reflection exceeds 500 bytes")
	}
	card, err := s.store.GetCard(ctx, cardID)
	if err != nil {
		if err == ErrNotFound {
			return CheckinResult{}, apierr.NotFound("topic card not found")
		}
		return CheckinResult{}, err
	}
	if card.UserID != userID {
		return CheckinResult{}, apierr.PermissionDenied("topic card not owned by caller")
	}
	if card.DeletedAt != nil {
		return CheckinResult{}, apierr.NotFound("topic card not found")
	}
	now := s.now().UTC()
	if err := s.store.MarkCheckedIn(ctx, cardID, now); err != nil {
		if err == ErrConflict {
			return CheckinResult{}, apierr.Conflict("topic card already checked in")
		}
		if err == ErrNotFound {
			return CheckinResult{}, apierr.NotFound("topic card not found")
		}
		return CheckinResult{}, err
	}
	row := Checkin{
		ID:         s.newID(),
		CardID:     cardID,
		UserID:     userID,
		Reflection: strings.TrimSpace(reflection),
		CreatedAt:  now,
	}
	if err := s.store.InsertCheckin(ctx, row); err != nil {
		return CheckinResult{}, err
	}
	st, err := UpdateOnCheckin(ctx, s.store, userID, now)
	if err != nil {
		return CheckinResult{}, err
	}
	incCheckin()
	return CheckinResult{CheckinID: row.ID, StreakDays: st.CurrentStreak}, nil
}
