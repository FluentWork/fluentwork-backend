package voicegateway

import (
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/config"
)

const (
	defaultVoiceHTTPAddr = ":8081"
	defaultAppServerURL  = "http://127.0.0.1:8080"
	defaultIdleTimeout   = 2 * time.Minute
)

// Config holds voice-gateway process settings.
type Config struct {
	HTTPAddr             string
	AppEnv               string
	AppServerInternalURL string
	InternalAPIToken     string
	IdleTimeout          time.Duration
}

// LoadConfig reads voice-gateway configuration from the environment.
// APP_ENV has no implicit default; local scripts must set it explicitly.
func LoadConfig() Config {
	appEnv := strings.TrimSpace(os.Getenv("APP_ENV"))
	token := strings.TrimSpace(os.Getenv("INTERNAL_API_TOKEN"))
	if token == "" && isDevelopmentEnv(appEnv) {
		token = config.DevInternalAPIToken
	}
	return Config{
		HTTPAddr:             envOr("VOICE_GATEWAY_HTTP_ADDR", defaultVoiceHTTPAddr),
		AppEnv:               appEnv,
		AppServerInternalURL: envOr("APP_SERVER_INTERNAL_URL", defaultAppServerURL),
		InternalAPIToken:     token,
		IdleTimeout:          durationOr("VOICE_GATEWAY_IDLE_TIMEOUT", defaultIdleTimeout),
	}
}

// IsDevelopment reports whether the process is an explicitly configured local/test environment.
func (c Config) IsDevelopment() bool {
	return isDevelopmentEnv(c.AppEnv)
}

// Validate checks required gateway settings.
func (c Config) Validate() error {
	if strings.TrimSpace(c.HTTPAddr) == "" {
		return fmt.Errorf("VOICE_GATEWAY_HTTP_ADDR is required")
	}
	if strings.TrimSpace(c.AppEnv) == "" {
		return fmt.Errorf("APP_ENV is required")
	}
	if strings.TrimSpace(c.AppServerInternalURL) == "" {
		return fmt.Errorf("APP_SERVER_INTERNAL_URL is required")
	}
	if len(strings.TrimSpace(c.InternalAPIToken)) < 16 {
		return fmt.Errorf("INTERNAL_API_TOKEN must be at least 16 characters")
	}
	if !c.IsDevelopment() && (c.InternalAPIToken == "" || c.InternalAPIToken == config.DevInternalAPIToken) {
		return fmt.Errorf("INTERNAL_API_TOKEN must be set outside development environments")
	}
	if c.IdleTimeout <= 0 {
		return fmt.Errorf("VOICE_GATEWAY_IDLE_TIMEOUT must be positive")
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

func isDevelopmentEnv(appEnv string) bool {
	switch strings.ToLower(strings.TrimSpace(appEnv)) {
	case "development", "dev", "test", "local":
		return true
	default:
		return false
	}
}
