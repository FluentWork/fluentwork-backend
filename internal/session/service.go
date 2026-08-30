package session

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"encoding/json"
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
		logger: logger.With("component", "session.service"),
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

// Activate marks a practice session active after WSS session.start.
func (s *Service) Activate(ctx context.Context, sessionID string) (ActivateResponse, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return ActivateResponse{}, apierr.InvalidArgument("session_id is required")
	}
	now := s.now().UTC()
	session, err := s.store.MarkSessionActive(ctx, sessionID, now)
	if err != nil {
		switch {
		case errors.Is(err, ErrNotFound):
			return ActivateResponse{}, apierr.NotFound("session not found")
		case errors.Is(err, ErrConflict):
			return ActivateResponse{}, apierr.Conflict("session cannot be activated")
		default:
			return ActivateResponse{}, err
		}
	}
	return ActivateResponse{SessionID: session.ID, Status: session.Status}, nil
}

// End persists session.end status and transcript rows (idempotent if already ended).
func (s *Service) End(ctx context.Context, req EndRequest) (EndResponse, error) {
	sessionID := strings.TrimSpace(req.SessionID)
	if sessionID == "" {
		return EndResponse{}, apierr.InvalidArgument("session_id is required")
	}
	existing, err := s.store.GetSession(ctx, sessionID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return EndResponse{}, apierr.NotFound("session not found")
		}
		return EndResponse{}, err
	}
	now := s.now().UTC()
	if existing.Status == StatusReviewed {
		saved, listErr := s.store.ListUtterances(ctx, sessionID)
		if listErr != nil {
			return EndResponse{}, listErr
		}
		return EndResponse{
			SessionID:      existing.ID,
			Status:         existing.Status,
			DurationSec:    existing.DurationSec,
			UtteranceCount: len(saved),
			AlreadyEnded:   true,
		}, nil
	}
	if existing.Status == StatusEnded {
		saved, listErr := s.store.ListUtterances(ctx, sessionID)
		if listErr != nil {
			return EndResponse{}, listErr
		}
		// Compensate a prior End that committed before enqueue failed.
		if err := s.ensureSessionFinishedEnqueued(ctx, sessionID, now); err != nil {
			return EndResponse{}, err
		}
		return EndResponse{
			SessionID:      existing.ID,
			Status:         existing.Status,
			DurationSec:    existing.DurationSec,
			UtteranceCount: len(saved),
			AlreadyEnded:   true,
		}, nil
	}

	utterances, err := s.normalizeEndUtterances(sessionID, req.Utterances, now)
	if err != nil {
		return EndResponse{}, err
	}
	session, saved, alreadyEnded, err := s.store.EndSession(ctx, sessionID, req.DurationSec, utterances, now)
	if err != nil {
		switch {
		case errors.Is(err, ErrNotFound):
			return EndResponse{}, apierr.NotFound("session not found")
		case errors.Is(err, ErrConflict):
			return EndResponse{}, apierr.Conflict("session cannot be ended")
		default:
			return EndResponse{}, err
		}
	}
	s.logger.Info("practice session ended",
		"session_id", session.ID,
		"user_id", session.UserID,
		"duration_sec", session.DurationSec,
		"utterance_count", len(saved),
		"already_ended", alreadyEnded,
		"reason", strings.TrimSpace(req.Reason),
	)
	if err := s.ensureSessionFinishedEnqueued(ctx, session.ID, now); err != nil {
		return EndResponse{}, err
	}
	return EndResponse{
		SessionID:      session.ID,
		Status:         session.Status,
		DurationSec:    session.DurationSec,
		UtteranceCount: len(saved),
		AlreadyEnded:   alreadyEnded,
	}, nil
}

// GetReview returns the review poll payload for the session owner (B6).
// Status is pending | ready | failed.
func (s *Service) GetReview(ctx context.Context, userID, sessionID string) (ReviewPollResponse, error) {
	userID = strings.TrimSpace(userID)
	sessionID = strings.TrimSpace(sessionID)
	if userID == "" {
		return ReviewPollResponse{}, apierr.Unauthenticated("missing authenticated user")
	}
	if sessionID == "" {
		return ReviewPollResponse{}, apierr.InvalidArgument("session_id is required")
	}

	session, err := s.store.GetSession(ctx, sessionID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return ReviewPollResponse{}, apierr.NotFound("session not found")
		}
		return ReviewPollResponse{}, err
	}
	if session.UserID != userID {
		// Hide existence from non-owners.
		return ReviewPollResponse{}, apierr.NotFound("session not found")
	}
	if session.Status == StatusAbandoned {
		return ReviewPollResponse{}, apierr.NotFound("session not found")
	}

	if session.Status == StatusReviewed {
		review := json.RawMessage(append([]byte(nil), session.ReviewJSON...))
		if len(review) == 0 {
			review = json.RawMessage(`{}`)
		}
		return ReviewPollResponse{
			SessionID: session.ID,
			Status:    ReviewPollReady,
			Review:    review,
		}, nil
	}

	failed, err := s.store.HasSessionJob(ctx, sessionID, JobTypeSessionFinished, JobStatusFailed)
	if err != nil {
		return ReviewPollResponse{}, err
	}
	if failed {
		return ReviewPollResponse{
			SessionID: session.ID,
			Status:    ReviewPollFailed,
		}, nil
	}

	return ReviewPollResponse{
		SessionID: session.ID,
		Status:    ReviewPollPending,
	}, nil
}

// PostMessage handles the degraded text channel stub (B7).
// Voice-preferred clients (channel != "text") receive CONFLICT.
func (s *Service) PostMessage(ctx context.Context, userID, sessionID string, req PostMessageRequest) (PostMessageResponse, error) {
	userID = strings.TrimSpace(userID)
	sessionID = strings.TrimSpace(sessionID)
	if userID == "" {
		return PostMessageResponse{}, apierr.Unauthenticated("missing authenticated user")
	}
	if sessionID == "" {
		return PostMessageResponse{}, apierr.InvalidArgument("session_id is required")
	}

	text := strings.TrimSpace(req.Text)
	if text == "" {
		return PostMessageResponse{}, apierr.InvalidArgument("text is required")
	}
	if len(text) > 4*1024 {
		return PostMessageResponse{}, apierr.InvalidArgument("text is too long")
	}

	channel := strings.TrimSpace(strings.ToLower(req.Channel))
	if channel != MessageChannelText {
		return PostMessageResponse{}, apierr.Conflict("voice channel is available")
	}

	session, err := s.store.GetSession(ctx, sessionID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return PostMessageResponse{}, apierr.NotFound("session not found")
		}
		return PostMessageResponse{}, err
	}
	if session.UserID != userID {
		return PostMessageResponse{}, apierr.NotFound("session not found")
	}
	switch session.Status {
	case StatusCreated, StatusActive:
		// open for degrade stub
	default:
		return PostMessageResponse{}, apierr.Conflict("session is not open for messages")
	}

	return PostMessageResponse{
		SessionID: session.ID,
		Reply:     "Text fallback is active. Full model replies land with the production degrade path.",
		Channel:   MessageChannelText,
		Generator: "stub-text-v1",
	}, nil
}

func (s *Service) ensureSessionFinishedEnqueued(ctx context.Context, sessionID string, now time.Time) error {
	exists, err := s.store.HasSessionJob(ctx, sessionID, JobTypeSessionFinished,
		JobStatusPending, JobStatusProcessing, JobStatusDone)
	if err != nil {
		return err
	}
	if exists {
		return nil
	}
	return s.enqueueSessionFinished(ctx, sessionID, now)
}

func (s *Service) enqueueSessionFinished(ctx context.Context, sessionID string, now time.Time) error {
	job := Job{
		ID:          s.newID(),
		SessionID:   sessionID,
		JobType:     JobTypeSessionFinished,
		Status:      JobStatusPending,
		Attempts:    0,
		AvailableAt: now,
		CreatedAt:   now,
		UpdatedAt:   now,
	}
	if err := s.store.EnqueueJob(ctx, job); err != nil {
		return err
	}
	s.logger.Info("session.finished enqueued", "session_id", sessionID, "job_id", job.ID)
	return nil
}

func (s *Service) normalizeEndUtterances(sessionID string, items []EndUtteranceItem, now time.Time) ([]Utterance, error) {
	const maxUtterances = 500
	if len(items) == 0 {
		return nil, nil
	}
	if len(items) > maxUtterances {
		return nil, apierr.InvalidArgument("too many utterances")
	}
	seen := make(map[int]struct{}, len(items))
	out := make([]Utterance, 0, len(items))
	for _, item := range items {
		if item.Seq < 1 || item.Seq > maxUtterances {
			return nil, apierr.InvalidArgument("utterance seq is out of range")
		}
		if _, ok := seen[item.Seq]; ok {
			return nil, apierr.InvalidArgument("utterance seq must be unique")
		}
		seen[item.Seq] = struct{}{}
		speaker := strings.TrimSpace(item.Speaker)
		switch speaker {
		case SpeakerUser, SpeakerAI:
		default:
			return nil, apierr.InvalidArgument("utterance speaker must be user or ai")
		}
		text := strings.TrimSpace(item.Text)
		if text == "" {
			return nil, apierr.InvalidArgument("utterance text is required")
		}
		if len(text) > 32*1024 {
			return nil, apierr.InvalidArgument("utterance text is too long")
		}
		out = append(out, Utterance{
			ID:        s.newID(),
			SessionID: sessionID,
			Seq:       item.Seq,
			Speaker:   speaker,
			Text:      text,
			CreatedAt: now,
		})
	}
	return out, nil
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
