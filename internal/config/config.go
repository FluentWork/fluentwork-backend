// Package config loads app-server runtime configuration from the environment.
package config

import (
	"fmt"
	"os"
	"strings"
	"time"
)

// DevJWTSecret is the local-only signing secret. Production must override it.
const DevJWTSecret = "fluentwork-dev-jwt-secret-change-me!!"

const (
	defaultHTTPAddr           = ":8080"
	defaultAppEnv             = "development"
	defaultAccessTokenTTL     = 2 * time.Hour
	defaultRefreshTokenTTL    = 30 * 24 * time.Hour
	defaultVoiceGatewayWSSURL = "ws://127.0.0.1:8081/v1/voice"
	defaultSessionTicketTTL   = 60 * time.Second
)

// Config holds process settings for app-server.
type Config struct {
	HTTPAddr           string
	AppEnv             string
	MySQLDSN           string
	AuthJWTSecret      string
	AccessTokenTTL     time.Duration
	RefreshTokenTTL    time.Duration
	VoiceGatewayWSSURL string
	SessionTicketTTL   time.Duration
}

// Load reads configuration from environment variables.
func Load() Config {
	return Config{
		HTTPAddr:           envOr("HTTP_ADDR", defaultHTTPAddr),
		AppEnv:             envOr("APP_ENV", defaultAppEnv),
		MySQLDSN:           strings.TrimSpace(os.Getenv("MYSQL_DSN")),
		AuthJWTSecret:      envOr("AUTH_JWT_SECRET", DevJWTSecret),
		AccessTokenTTL:     durationOr("AUTH_ACCESS_TTL", defaultAccessTokenTTL),
		RefreshTokenTTL:    durationOr("AUTH_REFRESH_TTL", defaultRefreshTokenTTL),
		VoiceGatewayWSSURL: envOr("VOICE_GATEWAY_WSS_URL", defaultVoiceGatewayWSSURL),
		SessionTicketTTL:   durationOr("SESSION_TICKET_TTL", defaultSessionTicketTTL),
	}
}

// IsProduction reports whether the process is running in production.
func (c Config) IsProduction() bool {
	return strings.EqualFold(c.AppEnv, "production")
}

// IsDevelopment reports whether the process is a local/test environment.
func (c Config) IsDevelopment() bool {
	switch strings.ToLower(strings.TrimSpace(c.AppEnv)) {
	case "development", "dev", "test", "local":
		return true
	default:
		return false
	}
}

// Validate checks required production and auth constraints.
func (c Config) Validate() error {
	if strings.TrimSpace(c.HTTPAddr) == "" {
		return fmt.Errorf("HTTP_ADDR is required")
	}
	if len(c.AuthJWTSecret) < 32 {
		return fmt.Errorf("AUTH_JWT_SECRET must be at least 32 characters")
	}
	if !c.IsDevelopment() && c.AuthJWTSecret == DevJWTSecret {
		return fmt.Errorf("AUTH_JWT_SECRET must be set outside development environments")
	}
	if c.IsProduction() && c.MySQLDSN == "" {
		return fmt.Errorf("MYSQL_DSN is required in production")
	}
	if c.AccessTokenTTL <= 0 {
		return fmt.Errorf("AUTH_ACCESS_TTL must be positive")
	}
	if c.RefreshTokenTTL <= 0 {
		return fmt.Errorf("AUTH_REFRESH_TTL must be positive")
	}
	if strings.TrimSpace(c.VoiceGatewayWSSURL) == "" {
		return fmt.Errorf("VOICE_GATEWAY_WSS_URL is required")
	}
	if c.SessionTicketTTL <= 0 {
		return fmt.Errorf("SESSION_TICKET_TTL must be positive")
	}
	return nil
}

func envOr(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func durationOr(key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return fallback
	}
	return parsed
}
