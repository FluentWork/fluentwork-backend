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

// DevInternalAPIToken is the local-only shared secret between app-server and voice-gateway.
const DevInternalAPIToken = "fluentwork-dev-internal-token-change-me!!"

const (
	defaultHTTPAddr           = ":8080"
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
	InternalAPIToken   string
	ArkBaseURL         string
	ArkAPIKey          string
	ArkReviewRefineEP  string
}

// Load reads configuration from environment variables.
// APP_ENV has no implicit default: local scripts must set it explicitly so a
// forgotten production deploy cannot silently treat the process as development.
// Development secrets are only auto-filled when APP_ENV is an explicit local value.
func Load() Config {
	appEnv := strings.TrimSpace(os.Getenv("APP_ENV"))
	return Config{
		HTTPAddr:           envOr("HTTP_ADDR", defaultHTTPAddr),
		AppEnv:             appEnv,
		MySQLDSN:           strings.TrimSpace(os.Getenv("MYSQL_DSN")),
		AuthJWTSecret:      secretOr("AUTH_JWT_SECRET", DevJWTSecret, appEnv),
		AccessTokenTTL:     durationOr("AUTH_ACCESS_TTL", defaultAccessTokenTTL),
		RefreshTokenTTL:    durationOr("AUTH_REFRESH_TTL", defaultRefreshTokenTTL),
		VoiceGatewayWSSURL: envOr("VOICE_GATEWAY_WSS_URL", defaultVoiceGatewayWSSURL),
		SessionTicketTTL:   durationOr("SESSION_TICKET_TTL", defaultSessionTicketTTL),
		InternalAPIToken:   secretOr("INTERNAL_API_TOKEN", DevInternalAPIToken, appEnv),
		ArkBaseURL:         envOr("ARK_BASE_URL", "https://ark.cn-beijing.volces.com/api/v3"),
		ArkAPIKey:          firstNonEmpty(strings.TrimSpace(os.Getenv("ARK_API_KEY")), strings.TrimSpace(os.Getenv("ARK_API_KEY_DEV"))),
		ArkReviewRefineEP:  strings.TrimSpace(os.Getenv("ARK_EP_REVIEW_REFINE")),
	}
}

// IsProduction reports whether the process is running in production.
func (c Config) IsProduction() bool {
	return strings.EqualFold(c.AppEnv, "production")
}

// IsDevelopment reports whether the process is an explicitly configured local/test environment.
func (c Config) IsDevelopment() bool {
	return isDevelopmentEnv(c.AppEnv)
}

// Validate checks required production and auth constraints.
func (c Config) Validate() error {
	if strings.TrimSpace(c.HTTPAddr) == "" {
		return fmt.Errorf("HTTP_ADDR is required")
	}
	if strings.TrimSpace(c.AppEnv) == "" {
		return fmt.Errorf("APP_ENV is required")
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
	if len(strings.TrimSpace(c.InternalAPIToken)) < 16 {
		return fmt.Errorf("INTERNAL_API_TOKEN must be at least 16 characters")
	}
	if !c.IsDevelopment() && (c.InternalAPIToken == "" || c.InternalAPIToken == DevInternalAPIToken) {
		return fmt.Errorf("INTERNAL_API_TOKEN must be set outside development environments")
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

func secretOr(key, devFallback, appEnv string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value != "" {
		return value
	}
	if isDevelopmentEnv(appEnv) {
		return devFallback
	}
	return ""
}

func isDevelopmentEnv(appEnv string) bool {
	switch strings.ToLower(strings.TrimSpace(appEnv)) {
	case "development", "dev", "test", "local":
		return true
	default:
		return false
	}
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

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
