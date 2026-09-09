package topic

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/FluentWork/fluentwork-backend/internal/apierr"
)

// Service lists today's cards and records checkins.
type Service struct {
	store  Store
	gen    *Generator
	logger *slog.Logger
	now    func() time.Time
	newID  func() string
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
	return ListResponse{Items: items}, nil
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
