package voicegateway_test

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/config"
	"github.com/FluentWork/fluentwork-backend/internal/voicegateway"
)

// os helpers — small wrappers so tests stay readable without pulling in
// extra packages.
func getwd() (string, error)              { return os.Getwd() }
func readFile(path string) ([]byte, error) { return os.ReadFile(path) }

func TestLoadConfigDefaults(t *testing.T) {
	t.Setenv("VOICE_GATEWAY_HTTP_ADDR", "")
	t.Setenv("APP_ENV", "development")
	t.Setenv("APP_SERVER_INTERNAL_URL", "")
	t.Setenv("INTERNAL_API_TOKEN", "")
	t.Setenv("VOICE_GATEWAY_IDLE_TIMEOUT", "")
	t.Setenv("VOICE_GATEWAY_PROVIDER", "mock")
	t.Setenv("VOICE_GATEWAY_CLIENT_AUDIO_FORMAT", "opus-framed")
	t.Setenv("VOICE_GATEWAY_VOLC_SPEECH_API_KEY", "")
	t.Setenv("VOLC_POC_API_KEY", "")
	t.Setenv("VOLC_SPEECH_API_KEY", "")
	t.Setenv("VOLC_SPEECH_API_KEY_DEV", "")

	cfg := voicegateway.LoadConfig()
	if cfg.HTTPAddr != ":8081" {
		t.Fatalf("HTTPAddr = %q", cfg.HTTPAddr)
	}
	if cfg.AppServerInternalURL != "http://127.0.0.1:8080" {
		t.Fatalf("AppServerInternalURL = %q", cfg.AppServerInternalURL)
	}
	if cfg.Provider != "mock" {
		t.Fatalf("Provider = %q", cfg.Provider)
	}
	if cfg.ClientAudioFormat != "opus-framed" {
		t.Fatalf("ClientAudioFormat = %q", cfg.ClientAudioFormat)
	}
	if cfg.InternalAPIToken != config.DevInternalAPIToken {
		t.Fatalf("InternalAPIToken = %q", cfg.InternalAPIToken)
	}
	if cfg.IdleTimeout != 2*time.Minute {
		t.Fatalf("IdleTimeout = %s", cfg.IdleTimeout)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
}

func TestConfigRejectsUnknownProvider(t *testing.T) {
	cfg := voicegateway.Config{
		HTTPAddr:             ":8081",
		AppEnv:               "development",
		AppServerInternalURL: "http://127.0.0.1:8080",
		InternalAPIToken:     config.DevInternalAPIToken,
		Provider:             "bogus",
		IdleTimeout:          2 * time.Minute,
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected provider validation error")
	}
}

func TestConfigRequiresSpeechKeyForVolcProvider(t *testing.T) {
	cfg := voicegateway.Config{
		HTTPAddr:             ":8081",
		AppEnv:               "development",
		AppServerInternalURL: "http://127.0.0.1:8080",
		InternalAPIToken:     config.DevInternalAPIToken,
		Provider:             "volc-duplex",
		ClientAudioFormat:    "opus-framed",
		IdleTimeout:          2 * time.Minute,
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected missing speech key error")
	}
}

func TestConfigAcceptsVolcProviderWithSpeechKey(t *testing.T) {
	cfg := voicegateway.Config{
		HTTPAddr:             ":8081",
		AppEnv:               "development",
		AppServerInternalURL: "http://127.0.0.1:8080",
		InternalAPIToken:     config.DevInternalAPIToken,
		Provider:             "volc-duplex",
		ClientAudioFormat:    "pcm-s16le",
		VolcSpeechAPIKey:     "api-key-1234567890",
		IdleTimeout:          2 * time.Minute,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

func TestLoadConfigPicksVolcSpeechDefaults(t *testing.T) {
	// Block .env.volc.local from injecting real values — the test owns the env.
	for _, k := range []string{
		"VOICE_GATEWAY_PROVIDER",
		"VOICE_GATEWAY_CLIENT_AUDIO_FORMAT",
		"VOICE_GATEWAY_VOLC_SPEECH_API_KEY",
		"VOLC_POC_API_KEY",
		"VOLC_SPEECH_API_KEY",
		"VOLC_SPEECH_API_KEY_DEV",
		"VOLC_POC_ENDPOINT",
		"VOLC_DUPLEX_MODEL",
		"VOLC_DUPLEX_VOICE",
	} {
		t.Setenv(k, "")
	}
	t.Setenv("APP_ENV", "development")
	t.Setenv("VOICE_GATEWAY_PROVIDER", "volc-duplex")
	t.Setenv("VOICE_GATEWAY_CLIENT_AUDIO_FORMAT", "pcm-s16le")
	t.Setenv("VOLC_SPEECH_API_KEY_DEV", "dev-key")
	t.Setenv("VOLC_POC_ENDPOINT", "wss://duplex.example")
	t.Setenv("VOLC_DUPLEX_MODEL", "model-x")
	t.Setenv("VOLC_DUPLEX_VOICE", "voice-y")

	cfg := voicegateway.LoadConfig()
	if cfg.VolcSpeechAPIKey != "dev-key" {
		t.Fatalf("VolcSpeechAPIKey = %q", cfg.VolcSpeechAPIKey)
	}
	if cfg.VolcDuplexEndpoint != "wss://duplex.example" {
		t.Fatalf("VolcDuplexEndpoint = %q", cfg.VolcDuplexEndpoint)
	}
	if cfg.VolcDuplexModel != "model-x" {
		t.Fatalf("VolcDuplexModel = %q", cfg.VolcDuplexModel)
	}
	if cfg.VolcDuplexVoice != "voice-y" {
		t.Fatalf("VolcDuplexVoice = %q", cfg.VolcDuplexVoice)
	}
}

func TestConfigRejectsDevTokenOutsideDevelopment(t *testing.T) {
	cfg := voicegateway.Config{
		HTTPAddr:             ":8081",
		AppEnv:               "production",
		AppServerInternalURL: "http://app-server:8080",
		InternalAPIToken:     config.DevInternalAPIToken,
		IdleTimeout:          2 * time.Minute,
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected production token error")
	}
}

func TestConfigRequiresAppEnv(t *testing.T) {
	cfg := voicegateway.Config{
		HTTPAddr:             ":8081",
		AppEnv:               "",
		AppServerInternalURL: "http://127.0.0.1:8080",
		InternalAPIToken:     config.DevInternalAPIToken,
		IdleTimeout:          2 * time.Minute,
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected APP_ENV required error")
	}
}

func TestLoadConfigDoesNotDefaultTokenOutsideDevelopment(t *testing.T) {
	t.Setenv("APP_ENV", "staging")
	t.Setenv("INTERNAL_API_TOKEN", "")
	cfg := voicegateway.LoadConfig()
	if cfg.InternalAPIToken != "" {
		t.Fatalf("token should stay empty outside development, got %q", cfg.InternalAPIToken)
	}
}

// TestLoadConfigPicksVolcDuplexFromVolcLocal verifies that a real
// .env.volc.local present at the repo root flips the gateway into
// volc-duplex mode (Provider=volc-duplex, ClientAudioFormat=pcm-s16le,
// VolcSpeechAPIKey populated) without any explicit shell exports.
// The test relies on the committed .env.volc.local — when it's missing the
// assertion degrades to a Skip instead of a failure.
//
// Crucial: this test must NOT t.Setenv the volc-related vars, because
// LoadConfig honours any pre-set value over the dotenv file. We only set
// APP_ENV so the dev token path stays inert.
func TestLoadConfigPicksVolcDuplexFromVolcLocal(t *testing.T) {
	// Defensive: ensure no shell export of these vars shadows the dotenv file.
	// Use os.Unsetenv + t.Cleanup since t.Setenv cannot unset a variable.
	for _, k := range []string{
		"VOICE_GATEWAY_PROVIDER",
		"VOICE_GATEWAY_CLIENT_AUDIO_FORMAT",
		"VOICE_GATEWAY_VOLC_SPEECH_API_KEY",
		"VOLC_POC_API_KEY",
		"VOLC_SPEECH_API_KEY",
		"VOLC_SPEECH_API_KEY_DEV",
		"VOLC_POC_ENDPOINT",
		"VOLC_DUPLEX_MODEL",
		"VOLC_DUPLEX_VOICE",
	} {
		orig, had := os.LookupEnv(k)
		_ = os.Unsetenv(k)
		k := k
		t.Cleanup(func() {
			if had {
				_ = os.Setenv(k, orig)
			} else {
				_ = os.Unsetenv(k)
			}
		})
	}

	root := findBackendRootOrSkip(t)
	dotenvPath := filepath.Join(root, ".env.volc.local")
	if _, err := readFile(dotenvPath); err != nil {
		t.Skipf(".env.volc.local missing at %s; skipping live-volc check", dotenvPath)
	}

	cfg := voicegateway.LoadConfig()
	if cfg.Provider != "volc-duplex" {
		t.Fatalf("Provider = %q, want volc-duplex (loaded from %s)", cfg.Provider, dotenvPath)
	}
	if cfg.ClientAudioFormat != "pcm-s16le" {
		t.Fatalf("ClientAudioFormat = %q, want pcm-s16le", cfg.ClientAudioFormat)
	}
	if cfg.VolcSpeechAPIKey == "" {
		t.Fatalf("VolcSpeechAPIKey must be populated from .env.volc.local")
	}
}

// TestLoadConfigEnvStillWinsOverDotenv verifies that an explicit shell export
// overrides whatever the dotenv file would have supplied — important for CI
// and for ad-hoc test runs.
func TestLoadConfigEnvStillWinsOverDotenv(t *testing.T) {
	// Force a different value via the shell; LoadConfig must not undo it.
	t.Setenv("VOICE_GATEWAY_PROVIDER", "mock")
	t.Setenv("APP_ENV", "development")
	t.Setenv("VOICE_GATEWAY_CLIENT_AUDIO_FORMAT", "opus-framed")
	t.Setenv("VOICE_GATEWAY_VOLC_SPEECH_API_KEY", "shell-key")

	cfg := voicegateway.LoadConfig()
	if cfg.Provider != "mock" {
		t.Fatalf("Provider = %q, want mock (shell export wins)", cfg.Provider)
	}
	if cfg.VolcSpeechAPIKey != "shell-key" {
		t.Fatalf("VolcSpeechAPIKey = %q, want shell-key", cfg.VolcSpeechAPIKey)
	}
}

// findBackendRootOrSkip locates the backend module root (the directory
// containing internal/voicegateway). Skips the test when run from an
// environment where the root cannot be discovered.
func findBackendRootOrSkip(t *testing.T) string {
	t.Helper()
	dir, err := getwd()
	if err != nil {
		t.Skipf("cannot determine cwd: %v", err)
	}
	for {
		if _, err := readFile(filepath.Join(dir, "go.mod")); err == nil {
			if _, err := readFile(filepath.Join(dir, "internal", "voicegateway", "config.go")); err == nil {
				return dir
			}
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Skip("backend module root not found from cwd")
		}
		dir = parent
	}
}
