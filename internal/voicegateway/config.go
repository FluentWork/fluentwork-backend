package voicegateway

import (
	"fmt"
	"os"
	"strings"
)

const (
	defaultVoiceHTTPAddr    = ":8081"
	defaultAppServerURL     = "http://127.0.0.1:8080"
	defaultInternalAPIToken = "fluentwork-dev-internal-token-change-me!!"
	defaultVoiceAppEnv      = "development"
)

// Config holds voice-gateway process settings.
type Config struct {
	HTTPAddr             string
	AppEnv               string
	AppServerInternalURL string
	InternalAPIToken     string
}

// LoadConfig reads voice-gateway configuration from the environment.
func LoadConfig() Config {
	return Config{
		HTTPAddr:             envOr("VOICE_GATEWAY_HTTP_ADDR", defaultVoiceHTTPAddr),
		AppEnv:               envOr("APP_ENV", defaultVoiceAppEnv),
		AppServerInternalURL: envOr("APP_SERVER_INTERNAL_URL", defaultAppServerURL),
		InternalAPIToken:     envOr("INTERNAL_API_TOKEN", defaultInternalAPIToken),
	}
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

// Validate checks required gateway settings.
func (c Config) Validate() error {
	if strings.TrimSpace(c.HTTPAddr) == "" {
		return fmt.Errorf("VOICE_GATEWAY_HTTP_ADDR is required")
	}
	if strings.TrimSpace(c.AppServerInternalURL) == "" {
		return fmt.Errorf("APP_SERVER_INTERNAL_URL is required")
	}
	if len(strings.TrimSpace(c.InternalAPIToken)) < 16 {
		return fmt.Errorf("INTERNAL_API_TOKEN must be at least 16 characters")
	}
	if !c.IsDevelopment() && c.InternalAPIToken == defaultInternalAPIToken {
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
