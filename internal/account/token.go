package account

import (
	"context"
	"crypto/rand"
	"crypto/sha256"
	"encoding/base64"
	"encoding/hex"
	"fmt"

	"github.com/golang-jwt/jwt/v5"

	"github.com/FluentWork/fluentwork-backend/internal/apierr"
)

const (
	tokenTypeBearer = "Bearer"
	tokenIssuer     = "fluentwork-app-server"
)

type accessClaims struct {
	IsGuest bool `json:"is_guest"`
	jwt.RegisteredClaims
}

func (s *Service) issueSession(ctx context.Context, user User) (TokenResponse, error) {
	now := s.now()
	expiresAt := now.Add(s.cfg.AccessTokenTTL)
	claims := accessClaims{
		IsGuest: user.IsGuest,
		RegisteredClaims: jwt.RegisteredClaims{
			Subject:   user.ID,
			Issuer:    tokenIssuer,
			ID:        s.newID(),
			IssuedAt:  jwt.NewNumericDate(now),
			ExpiresAt: jwt.NewNumericDate(expiresAt),
		},
	}
	token := jwt.NewWithClaims(jwt.SigningMethodHS256, claims)
	access, err := token.SignedString([]byte(s.cfg.AuthJWTSecret))
	if err != nil {
		return TokenResponse{}, fmt.Errorf("sign access token: %w", err)
	}

	refreshRaw, err := randomToken()
	if err != nil {
		return TokenResponse{}, err
	}
	refresh := RefreshToken{
		ID:        s.newID(),
		UserID:    user.ID,
		Hash:      hashToken(refreshRaw),
		ExpiresAt: now.Add(s.cfg.RefreshTokenTTL),
		CreatedAt: now,
	}
	if err := s.store.ReplaceRefreshToken(ctx, refresh); err != nil {
		return TokenResponse{}, err
	}

	return TokenResponse{
		UserID:       user.ID,
		IsGuest:      user.IsGuest,
		Status:       user.Status,
		AccessToken:  access,
		RefreshToken: refreshRaw,
		TokenType:    tokenTypeBearer,
		ExpiresIn:    int64(s.cfg.AccessTokenTTL.Seconds()),
	}, nil
}

func (s *Service) parseAccessToken(token string) (accessClaims, error) {
	var claims accessClaims
	parsed, err := jwt.ParseWithClaims(token, &claims, func(t *jwt.Token) (any, error) {
		if t.Method != jwt.SigningMethodHS256 {
			return nil, fmt.Errorf("unexpected signing method")
		}
		return []byte(s.cfg.AuthJWTSecret), nil
	}, jwt.WithIssuer(tokenIssuer), jwt.WithValidMethods([]string{jwt.SigningMethodHS256.Alg()}))
	if err != nil || parsed == nil || !parsed.Valid {
		return accessClaims{}, apierr.Unauthenticated("invalid access token")
	}
	if claims.Subject == "" {
		return accessClaims{}, apierr.Unauthenticated("invalid access token")
	}
	return claims, nil
}

func randomToken() (string, error) {
	buf := make([]byte, 32)
	if _, err := rand.Read(buf); err != nil {
		return "", fmt.Errorf("generate token: %w", err)
	}
	return base64.RawURLEncoding.EncodeToString(buf), nil
}

func hashToken(raw string) string {
	sum := sha256.Sum256([]byte(raw))
	return hex.EncodeToString(sum[:])
}
