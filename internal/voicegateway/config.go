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
	Provider             string
	ClientAudioFormat    string
	VolcSpeechAPIKey     string
	VolcDuplexEndpoint   string
	VolcDuplexModel      string
	VolcDuplexVoice      string
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
		Provider:             envOr("VOICE_GATEWAY_PROVIDER", "mock"),
		ClientAudioFormat:    envOr("VOICE_GATEWAY_CLIENT_AUDIO_FORMAT", "opus-framed"),
		VolcSpeechAPIKey: envFirst(
			"VOICE_GATEWAY_VOLC_SPEECH_API_KEY",
			"VOLC_POC_API_KEY",
			"VOLC_SPEECH_API_KEY",
			"VOLC_SPEECH_API_KEY_DEV",
		),
		VolcDuplexEndpoint: envFirst(
			"VOICE_GATEWAY_VOLC_DUPLEX_ENDPOINT",
			"VOLC_POC_ENDPOINT",
		),
		VolcDuplexModel: envFirst(
			"VOICE_GATEWAY_VOLC_DUPLEX_MODEL",
			"VOLC_DUPLEX_MODEL",
		),
		VolcDuplexVoice: envFirst(
			"VOICE_GATEWAY_VOLC_DUPLEX_VOICE",
			"VOLC_DUPLEX_VOICE",
		),
		IdleTimeout: durationOr("VOICE_GATEWAY_IDLE_TIMEOUT", defaultIdleTimeout),
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
	switch strings.ToLower(strings.TrimSpace(c.Provider)) {
	case "mock", "volc-duplex":
	default:
		return fmt.Errorf("VOICE_GATEWAY_PROVIDER must be one of mock, volc-duplex")
	}
	switch strings.ToLower(strings.TrimSpace(c.ClientAudioFormat)) {
	case "opus-framed", "pcm-s16le":
	default:
		return fmt.Errorf("VOICE_GATEWAY_CLIENT_AUDIO_FORMAT must be one of opus-framed, pcm-s16le")
	}
	if strings.EqualFold(strings.TrimSpace(c.Provider), "volc-duplex") {
		if strings.TrimSpace(c.VolcSpeechAPIKey) == "" {
			return fmt.Errorf("VOICE_GATEWAY_PROVIDER=volc-duplex requires VOLC speech API key")
		}
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

func envFirst(keys ...string) string {
	for _, key := range keys {
		if value := strings.TrimSpace(os.Getenv(key)); value != "" {
			return value
		}
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
