package account

import (
	"context"
	"errors"
	"log/slog"
	"regexp"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/FluentWork/fluentwork-backend/internal/apierr"
	"github.com/FluentWork/fluentwork-backend/internal/config"
)

var deviceIDPattern = regexp.MustCompile(`^[A-Za-z0-9._:-]{1,128}$`)

// Reassigner moves guest-owned business rows onto a registered account.
// B2+ session/material/corpus modules should register idempotent implementations.
type Reassigner interface {
	ReassignFromGuest(ctx context.Context, guestUserID, targetUserID string) error
}

// NopReassigner is the B1 placeholder used until business tables exist.
type NopReassigner struct{}

// ReassignFromGuest implements Reassigner.
func (NopReassigner) ReassignFromGuest(context.Context, string, string) error {
	return nil
}

// ChainReassigner fans guest merge out to multiple business modules.
type ChainReassigner []Reassigner

// ReassignFromGuest implements Reassigner.
func (r ChainReassigner) ReassignFromGuest(ctx context.Context, guestUserID, targetUserID string) error {
	for _, item := range r {
		if item == nil {
			continue
		}
		if err := item.ReassignFromGuest(ctx, guestUserID, targetUserID); err != nil {
			return err
		}
	}
	return nil
}

// Service implements guest issuance and account merge.
type Service struct {
	store      Store
	reassigner Reassigner
	cfg        config.Config
	logger     *slog.Logger
	now        func() time.Time
	newID      func() string
}

// NewService constructs the account service.
func NewService(store Store, reassigner Reassigner, cfg config.Config, logger *slog.Logger) *Service {
	if reassigner == nil {
		reassigner = NopReassigner{}
	}
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		store:      store,
		reassigner: reassigner,
		cfg:        cfg,
		logger:     logger.With("component", "account.service"),
		now:        time.Now,
		newID:      uuid.NewString,
	}
}

// IssueGuest creates or reuses a device-bound identity and returns tokens.
func (s *Service) IssueGuest(ctx context.Context, deviceID string) (TokenResponse, error) {
	s.logger.Info("🔑 IssueGuest", "device_id", deviceID)
	normalized, err := normalizeDeviceID(deviceID)
	if err != nil {
		s.logger.Warn("🔑 Invalid deviceID", "error", err)
		return TokenResponse{}, err
	}

	user, err := s.store.GetActiveByDeviceID(ctx, normalized)
	if errors.Is(err, ErrNotFound) {
		s.logger.Info("🔑 Creating new guest user")
		user, err = s.createGuest(ctx, normalized)
		if err != nil {
			existing, getErr := s.store.GetActiveByDeviceID(ctx, normalized)
			if getErr != nil {
				return TokenResponse{}, err
			}
			user = existing
		}
	} else if err != nil {
		return TokenResponse{}, err
	}

	tokens, err := s.issueSession(ctx, user)
	if err != nil {
		return TokenResponse{}, err
	}
	s.logger.Info("🔑 Guest identity issued", "user_id", user.ID, "is_guest", user.IsGuest, "token_prefix", tokens.AccessToken[:min(20, len(tokens.AccessToken))])
	return tokens, nil
}

// Merge moves guest-owned data onto the authenticated registered account.
func (s *Service) Merge(ctx context.Context, actorUserID, deviceID string) (MergeResponse, error) {
	normalized, err := normalizeDeviceID(deviceID)
	if err != nil {
		return MergeResponse{}, err
	}
	actor, err := s.store.GetUser(ctx, actorUserID)
	if errors.Is(err, ErrNotFound) {
		return MergeResponse{}, apierr.Unauthenticated("invalid access token")
	}
	if err != nil {
		return MergeResponse{}, err
	}
	if actor.IsGuest {
		return MergeResponse{}, apierr.PermissionDenied("registered account required")
	}
	if actor.Status != UserStatusActive {
		return MergeResponse{}, apierr.Unauthenticated("account is not active")
	}

	holder, err := s.store.GetActiveByDeviceID(ctx, normalized)
	if errors.Is(err, ErrNotFound) {
		return MergeResponse{}, apierr.NotFound("guest identity not found")
	}
	if err != nil {
		return MergeResponse{}, err
	}
	if holder.ID == actor.ID {
		from := s.mergedFrom(ctx, actor.ID)
		if from != nil {
			_ = s.store.DeleteRefreshTokensForUser(ctx, *from)
		}
		return MergeResponse{
			UserID:           actor.ID,
			IsGuest:          false,
			MergedFromUserID: from,
			AlreadyMerged:    true,
		}, nil
	}
	if !holder.IsGuest {
		return MergeResponse{}, apierr.Conflict("device_id is bound to another account")
	}

	if err := s.reassigner.ReassignFromGuest(ctx, holder.ID, actor.ID); err != nil {
		return MergeResponse{}, err
	}
	if err := s.store.DeleteRefreshTokensForUser(ctx, holder.ID); err != nil {
		return MergeResponse{}, err
	}
	now := s.now()
	if err := s.store.MarkMerged(ctx, holder.ID, actor.ID, normalized, now); err != nil {
		return MergeResponse{}, err
	}

	s.logger.Info("guest account merged", "guest_user_id", holder.ID, "target_user_id", actor.ID)
	fromID := holder.ID
	return MergeResponse{
		UserID:           actor.ID,
		IsGuest:          false,
		MergedFromUserID: &fromID,
		AlreadyMerged:    false,
	}, nil
}

// Authenticate validates an access token and returns the active user.
func (s *Service) Authenticate(ctx context.Context, accessToken string) (User, error) {
	s.logger.Info("🔑 Authenticate", "token_prefix", accessToken[:min(20, len(accessToken))])
	claims, err := s.parseAccessToken(accessToken)
	if err != nil {
		s.logger.Warn("🔑 Failed to parse token", "error", err)
		return User{}, err
	}
	s.logger.Info("🔑 Token parsed", "user_id", claims.Subject)
	user, err := s.store.GetUser(ctx, claims.Subject)
	if errors.Is(err, ErrNotFound) {
		s.logger.Warn("🔑 User not found", "user_id", claims.Subject)
		return User{}, apierr.Unauthenticated("invalid access token")
	}
	if err != nil {
		s.logger.Error("🔑 GetUser error", "error", err)
		return User{}, err
	}
	if user.Status != UserStatusActive {
		s.logger.Warn("🔑 User not active", "user_id", user.ID, "status", user.Status)
		return User{}, apierr.Unauthenticated("account is not active")
	}
	s.logger.Info("🔑 Auth success", "user_id", user.ID, "is_guest", user.IsGuest)
	return user, nil
}

// IssueSession issues tokens for an existing user. Tests and later login use this.
func (s *Service) IssueSession(ctx context.Context, user User) (TokenResponse, error) {
	return s.issueSession(ctx, user)
}

func (s *Service) createGuest(ctx context.Context, deviceID string) (User, error) {
	now := s.now()
	user := User{
		ID:        s.newID(),
		DeviceID:  &deviceID,
		IsGuest:   true,
		Status:    UserStatusActive,
		CreatedAt: now,
		UpdatedAt: now,
	}
	if err := s.store.CreateUser(ctx, user); err != nil {
		return User{}, err
	}
	return user, nil
}

func (s *Service) mergedFrom(ctx context.Context, targetID string) *string {
	guest, err := s.store.FindGuestMergedInto(ctx, targetID)
	if err != nil {
		return nil
	}
	id := guest.ID
	return &id
}

func normalizeDeviceID(raw string) (string, error) {
	id := strings.TrimSpace(raw)
	if id == "" {
		return "", apierr.InvalidArgument("device_id is required")
	}
	if !deviceIDPattern.MatchString(id) {
		return "", apierr.InvalidArgument("device_id is invalid")
	}
	return id, nil
}
