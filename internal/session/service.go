package session

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"errors"
	"fmt"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/FluentWork/fluentwork-backend/internal/apierr"
	"github.com/FluentWork/fluentwork-backend/internal/config"
)

var (
	materialIDPattern = regexp.MustCompile(`^[A-Za-z0-9-]{1,36}$`)
	sceneTypePattern  = regexp.MustCompile(`^[A-Za-z0-9._-]{1,64}$`)
)

// Service creates practice sessions and issues WSS tickets.
type Service struct {
	store  Store
	cfg    config.Config
	logger *slog.Logger
	now    func() time.Time
	newID  func() string
}

// NewService constructs the session service.
func NewService(store Store, cfg config.Config, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		store:  store,
		cfg:    cfg,
		logger: logger,
		now:    time.Now,
		newID:  uuid.NewString,
	}
}

// Reassigner adapts Store to account.Reassigner for guest merge.
type Reassigner struct {
	Store Store
}

// ReassignFromGuest moves guest-owned sessions onto the registered account.
func (r Reassigner) ReassignFromGuest(ctx context.Context, guestUserID, targetUserID string) error {
	if r.Store == nil {
		return nil
	}
	return r.Store.ReassignUser(ctx, guestUserID, targetUserID)
}

// Create issues a practice session and a one-time WSS ticket.
func (s *Service) Create(ctx context.Context, userID string, req CreateRequest) (CreateResponse, error) {
	if strings.TrimSpace(userID) == "" {
		return CreateResponse{}, apierr.Unauthenticated("missing authenticated user")
	}
	materialID, err := normalizeOptionalMaterialID(req.MaterialID)
	if err != nil {
		return CreateResponse{}, err
	}
	sceneType, err := normalizeSceneType(req.SceneType)
	if err != nil {
		return CreateResponse{}, err
	}

	now := s.now().UTC()
	session := Session{
		ID:          s.newID(),
		UserID:      userID,
		MaterialID:  materialID,
		SceneType:   sceneType,
		Status:      StatusCreated,
		DurationSec: 0,
		CreatedAt:   now,
		UpdatedAt:   now,
	}

	rawTicket, err := randomTicket()
	if err != nil {
		return CreateResponse{}, err
	}
	expiresAt := now.Add(s.cfg.SessionTicketTTL)
	ticket := Ticket{
		ID:        s.newID(),
		SessionID: session.ID,
		UserID:    userID,
		Hash:      hashTicket(rawTicket),
		ExpiresAt: expiresAt,
		CreatedAt: now,
	}
	if err := s.store.CreateSessionWithTicket(ctx, session, ticket); err != nil {
		return CreateResponse{}, err
	}

	s.logger.Info("practice session created",
		"session_id", session.ID,
		"user_id", userID,
		"scene_type", sceneType,
		"ticket_expires_at", expiresAt,
	)

	return CreateResponse{
		SessionID:       session.ID,
		WSSURL:          s.cfg.VoiceGatewayWSSURL,
		Ticket:          rawTicket,
		TicketExpiresIn: int64(s.cfg.SessionTicketTTL.Seconds()),
		TicketExpiresAt: expiresAt,
		SceneType:       sceneType,
		Status:          session.Status,
	}, nil
}

// ConsumeTicket validates and atomically consumes a one-time WSS ticket (B3 handshake).
func (s *Service) ConsumeTicket(ctx context.Context, rawTicket string) (Ticket, error) {
	rawTicket = strings.TrimSpace(rawTicket)
	if rawTicket == "" {
		return Ticket{}, apierr.Unauthenticated("ticket is required")
	}
	now := s.now().UTC()
	ticket, err := s.store.ConsumeTicket(ctx, hashTicket(rawTicket), now)
	if err != nil {
		switch {
		case errors.Is(err, ErrNotFound):
			s.logger.Warn("session ticket not found")
			return Ticket{}, apierr.Unauthenticated("invalid ticket")
		case errors.Is(err, ErrTicketUsed):
			s.logger.Warn("session ticket already used", "session_id", ticket.SessionID, "user_id", ticket.UserID)
			return Ticket{}, apierr.Unauthenticated("ticket already used")
		case errors.Is(err, ErrTicketExpired):
			s.logger.Warn("session ticket expired", "session_id", ticket.SessionID, "user_id", ticket.UserID, "expires_at", ticket.ExpiresAt)
			return Ticket{}, apierr.Unauthenticated("ticket expired")
		default:
			return Ticket{}, err
		}
	}
	return ticket, nil
}

// LookupTicket is retained as an alias for ConsumeTicket for gateway call sites.
// Deprecated: prefer ConsumeTicket; lookup without consume is intentionally unsupported.
func (s *Service) LookupTicket(ctx context.Context, rawTicket string) (Ticket, error) {
	return s.ConsumeTicket(ctx, rawTicket)
}

func normalizeOptionalMaterialID(raw *string) (*string, error) {
	if raw == nil {
		return nil, nil
	}
	id := strings.TrimSpace(*raw)
	if id == "" {
		return nil, nil
	}
	if !materialIDPattern.MatchString(id) {
		return nil, apierr.InvalidArgument("material_id is invalid")
	}
	return &id, nil
}

func normalizeSceneType(raw string) (string, error) {
	scene := strings.TrimSpace(raw)
	if scene == "" {
		return DefaultSceneType, nil
	}
	if !sceneTypePattern.MatchString(scene) {
		return "", apierr.InvalidArgument("scene_type is invalid")
	}
	return scene, nil
}

func randomTicket() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate ticket: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func hashTicket(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
