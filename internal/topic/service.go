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

// RealUseLedger credits and reads confirmed real-world uses (86_ M10).
//
// The topic module owns the question ("did you use these with a real person?")
// and the corpus module owns the answer's bookkeeping; this is the seam between
// them, narrow on purpose.
type RealUseLedger interface {
	RecordRealUse(ctx context.Context, userID string, blockIDs []string, source, refID string) (int, error)
	CountRealUsesBySource(ctx context.Context, userID string, since time.Time) (map[string]int, error)
}

// SetRealUseLedger attaches the corpus-side ledger. Optional: without it a
// checkin still records the streak, it just cannot credit uses.
func (s *Service) SetRealUseLedger(ledger RealUseLedger) {
	if s == nil {
		return
	}
	s.realUses = ledger
}

// BlockLookup resolves a card's 话术块清单 for display (PRD §7.8 H1). Optional:
// without it the cards still list block ids.
type BlockLookup interface {
	ListBlocks(ctx context.Context, filter corpus.ListFilter) ([]corpus.PhraseBlock, error)
}

// Service lists today's cards and records checkins.
type Service struct {
	store    Store
	gen      *Generator
	blocks   BlockLookup
	realUses RealUseLedger
	logger   *slog.Logger
	now      func() time.Time
	newID    func() string
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

// Dismiss records why a card did not become a real conversation (86_ M11).
//
// It is the only negative signal the product can get about the last mile it
// cannot observe, and it is deliberately consequence-free: no streak change, no
// penalty, one reason per card. Saying "I did not dare" must be as easy as
// saying "I did".
func (s *Service) Dismiss(ctx context.Context, userID, cardID, reason string) (DismissResult, error) {
	userID = strings.TrimSpace(userID)
	cardID = strings.TrimSpace(cardID)
	if userID == "" {
		return DismissResult{}, apierr.Unauthenticated("missing authenticated user")
	}
	if cardID == "" {
		return DismissResult{}, apierr.InvalidArgument("card_id is required")
	}
	reason = strings.TrimSpace(reason)
	if !ValidDismissReason(reason) {
		return DismissResult{}, apierr.InvalidArgument("reason must be no_partner, not_confident, no_time or not_relevant")
	}
	card, err := s.store.GetCard(ctx, cardID)
	if err != nil {
		if err == ErrNotFound {
			return DismissResult{}, apierr.NotFound("topic card not found")
		}
		return DismissResult{}, err
	}
	// Another learner's card is NotFound, not Forbidden: saying it exists is the
	// leak (same rule as checkin, appeal and feedback).
	if card.UserID != userID || card.DeletedAt != nil {
		return DismissResult{}, apierr.NotFound("topic card not found")
	}
	first, err := s.store.MarkDismissed(ctx, cardID, reason, s.now().UTC())
	if err != nil {
		if err == ErrNotFound {
			return DismissResult{}, apierr.NotFound("topic card not found")
		}
		return DismissResult{}, err
	}
	if first {
		incDismiss(reason)
	}
	return DismissResult{CardID: cardID, Reason: reason, AlreadyDismissed: !first}, nil
}

// Checkin records a card completion.
func (s *Service) Checkin(ctx context.Context, userID, cardID string, req CheckinRequest) (CheckinResult, error) {
	reflection := req.Reflection
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
	result := CheckinResult{CheckinID: row.ID}

	// Credit the phrases the learner says they used. Only ids this card actually
	// offered can be credited: the card is the contract, so an id from elsewhere
	// is reported back instead of quietly accepted.
	if s.realUses != nil && len(req.UsedBlockIDs) > 0 {
		offered := make(map[string]struct{}, len(card.BlockIDs))
		for _, id := range card.BlockIDs {
			offered[id] = struct{}{}
		}
		allowed := make([]string, 0, len(req.UsedBlockIDs))
		for _, id := range req.UsedBlockIDs {
			if _, ok := offered[id]; ok {
				allowed = append(allowed, id)
				continue
			}
			result.IgnoredBlockIDs = append(result.IgnoredBlockIDs, id)
		}
		if len(allowed) > 0 {
			credited, err := s.realUses.RecordRealUse(ctx, userID, allowed, "checkin", row.ID)
			if err != nil {
				// The checkin is already recorded; losing the credit is a
				// bookkeeping problem, not a reason to fail the learner.
				s.logger.Warn("checkin real-use credit failed",
					"user_id", userID, "card_id", cardID, "err", err)
			}
			result.RecordedUse = credited
		}
	}
	st, err := UpdateOnCheckin(ctx, s.store, userID, now)
	if err != nil {
		return CheckinResult{}, err
	}
	incCheckin()
	result.StreakDays = st.CurrentStreak
	return result, nil
}
