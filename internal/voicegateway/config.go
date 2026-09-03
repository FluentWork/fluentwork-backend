package voicegateway

import (
	"fmt"
	"os"
	"path/filepath"
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
	// DevEchoText is the authoritative text the dev-echo provider returns
	// as ServerASRText on every user.speech.end. Only honored when
	// Provider == "dev-echo". Empty text makes the provider a no-op (logs
	// a warning on session open).
	DevEchoText string
	IdleTimeout time.Duration
}

// LoadConfig reads voice-gateway configuration from the environment.
// APP_ENV has no implicit default; local scripts must set it explicitly.
//
// Optional .env files are also loaded from the project root (if present):
//   - .env.dev      → base defaults for local development
//   - .env.volc.local → volcano/duobao credentials (gitignored, overrides .env.dev)
//
// Files are loaded in order; later files take precedence. Values exported
// to the environment by the shell still win over file values, so CI /
// container deployments don't need the dotenv files at all.
func LoadConfig() Config {
	root, _ := findProjectRoot()
	if root != "" {
		loadDotenvIfPresent(filepath.Join(root, ".env.dev"))
		loadDotenvIfPresent(filepath.Join(root, ".env.volc.local"))
	}

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
		DevEchoText:          envOr("VOICE_DEV_ECHO_TEXT", ""),
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
	case "mock", "volc-duplex", "dev-echo":
	default:
		return fmt.Errorf("VOICE_GATEWAY_PROVIDER must be one of mock, volc-duplex, dev-echo")
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

// findProjectRoot walks upward from the current working directory looking
// for the FluentWork backend module root (the directory containing go.mod).
// Returns "" when not found so callers can degrade gracefully.
func findProjectRoot() (string, error) {
	cwd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	dir := cwd
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			// Extra sanity check: voice-gateway should live here.
			if _, err := os.Stat(filepath.Join(dir, "internal", "voicegateway")); err == nil {
				return dir, nil
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			return "", nil
		}
		dir = parent
	}
}

// loadDotenvIfPresent reads KEY=VALUE lines from path into the process
// environment, but only when the variable is not already set. This lets
// real environment variables (CI / shell exports) win over file values,
// while still allowing a local .env.volc.local to seed defaults.
func loadDotenvIfPresent(path string) {
	if path == "" {
		return
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return // file missing / unreadable is non-fatal
	}
	for _, line := range strings.Split(string(data), "\n") {
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, "#") {
			continue
		}
		eq := strings.IndexByte(line, '=')
		if eq <= 0 {
			continue
		}
		key := strings.TrimSpace(line[:eq])
		value := strings.TrimSpace(line[eq+1:])
		// Strip surrounding quotes (single or double) if present.
		if len(value) >= 2 {
			first, last := value[0], value[len(value)-1]
			if (first == '"' && last == '"') || (first == '\'' && last == '\'') {
				value = value[1 : len(value)-1]
			}
		}
		if _, already := os.LookupEnv(key); already {
			continue
		}
		_ = os.Setenv(key, value)
	}
}
