package voicegateway_test

import (
	"testing"

	"github.com/FluentWork/fluentwork-backend/internal/voicegateway"
)

func TestLoadConfigDefaults(t *testing.T) {
	t.Setenv("VOICE_GATEWAY_HTTP_ADDR", "")
	t.Setenv("APP_ENV", "")
	t.Setenv("APP_SERVER_INTERNAL_URL", "")
	t.Setenv("INTERNAL_API_TOKEN", "")

	cfg := voicegateway.LoadConfig()
	if cfg.HTTPAddr != ":8081" {
		t.Fatalf("HTTPAddr = %q", cfg.HTTPAddr)
	}
	if cfg.AppServerInternalURL != "http://127.0.0.1:8080" {
		t.Fatalf("AppServerInternalURL = %q", cfg.AppServerInternalURL)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
}

func TestConfigRejectsDevTokenOutsideDevelopment(t *testing.T) {
	cfg := voicegateway.Config{
		HTTPAddr:             ":8081",
		AppEnv:               "production",
		AppServerInternalURL: "http://app-server:8080",
		InternalAPIToken:     "fluentwork-dev-internal-token-change-me!!",
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected production token error")
	}
}
