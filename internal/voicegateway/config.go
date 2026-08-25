package voicegateway

import (
	"fmt"
	"os"
	"strings"

	"github.com/FluentWork/fluentwork-backend/internal/config"
)

const (
	defaultVoiceHTTPAddr = ":8081"
	defaultAppServerURL  = "http://127.0.0.1:8080"
)

// Config holds voice-gateway process settings.
type Config struct {
	HTTPAddr             string
	AppEnv               string
	AppServerInternalURL string
	InternalAPIToken     string
}

// LoadConfig reads voice-gateway configuration from the environment.
// APP_ENV has no implicit default; local scripts must set it explicitly.
func LoadConfig() Config {
	return Config{
		HTTPAddr:             envOr("VOICE_GATEWAY_HTTP_ADDR", defaultVoiceHTTPAddr),
		AppEnv:               strings.TrimSpace(os.Getenv("APP_ENV")),
		AppServerInternalURL: envOr("APP_SERVER_INTERNAL_URL", defaultAppServerURL),
		InternalAPIToken:     envOr("INTERNAL_API_TOKEN", config.DevInternalAPIToken),
	}
}

// IsDevelopment reports whether the process is an explicitly configured local/test environment.
func (c Config) IsDevelopment() bool {
	switch strings.ToLower(strings.TrimSpace(c.AppEnv)) {
	case "development", "dev", "test", "local":
		return true
	default:
		return false
	}
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
	if !c.IsDevelopment() && c.InternalAPIToken == config.DevInternalAPIToken {
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
