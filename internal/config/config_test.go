package config

import (
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("HTTP_ADDR", "")
	t.Setenv("APP_ENV", "development")
	t.Setenv("MYSQL_DSN", "")
	t.Setenv("AUTH_JWT_SECRET", "")
	t.Setenv("AUTH_ACCESS_TTL", "")
	t.Setenv("AUTH_REFRESH_TTL", "")
	t.Setenv("VOICE_GATEWAY_WSS_URL", "")
	t.Setenv("SESSION_TICKET_TTL", "")
	t.Setenv("INTERNAL_API_TOKEN", "")
	t.Setenv("ARK_BASE_URL", "")
	t.Setenv("ARK_API_KEY", "")
	t.Setenv("ARK_API_KEY_DEV", "")
	t.Setenv("ARK_EP_REVIEW_REFINE", "")

	cfg := Load()
	if cfg.HTTPAddr != defaultHTTPAddr {
		t.Fatalf("HTTPAddr = %q", cfg.HTTPAddr)
	}
	if cfg.AppEnv != "development" {
		t.Fatalf("AppEnv = %q", cfg.AppEnv)
	}
	if cfg.AuthJWTSecret != DevJWTSecret {
		t.Fatalf("AuthJWTSecret = %q", cfg.AuthJWTSecret)
	}
	if cfg.AccessTokenTTL != defaultAccessTokenTTL {
		t.Fatalf("AccessTokenTTL = %s", cfg.AccessTokenTTL)
	}
	if cfg.RefreshTokenTTL != defaultRefreshTokenTTL {
		t.Fatalf("RefreshTokenTTL = %s", cfg.RefreshTokenTTL)
	}
	if cfg.VoiceGatewayWSSURL != defaultVoiceGatewayWSSURL {
		t.Fatalf("VoiceGatewayWSSURL = %q", cfg.VoiceGatewayWSSURL)
	}
	if cfg.SessionTicketTTL != defaultSessionTicketTTL {
		t.Fatalf("SessionTicketTTL = %s", cfg.SessionTicketTTL)
	}
	if cfg.InternalAPIToken != DevInternalAPIToken {
		t.Fatalf("InternalAPIToken = %q", cfg.InternalAPIToken)
	}
	if cfg.ArkBaseURL != "https://ark.cn-beijing.volces.com/api/v3" {
		t.Fatalf("ArkBaseURL = %q", cfg.ArkBaseURL)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
}

func TestValidateRequiresAppEnv(t *testing.T) {
	cfg := Config{
		HTTPAddr:           ":8080",
		AppEnv:             "",
		AuthJWTSecret:      DevJWTSecret,
		AccessTokenTTL:     time.Hour,
		RefreshTokenTTL:    24 * time.Hour,
		VoiceGatewayWSSURL: defaultVoiceGatewayWSSURL,
		SessionTicketTTL:   defaultSessionTicketTTL,
		InternalAPIToken:   DevInternalAPIToken,
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected APP_ENV required error")
	}
}

func TestValidateProductionRequiresMySQLAndSecret(t *testing.T) {
	cfg := Config{
		HTTPAddr:           ":8080",
		AppEnv:             "production",
		AuthJWTSecret:      DevJWTSecret,
		AccessTokenTTL:     time.Hour,
		RefreshTokenTTL:    24 * time.Hour,
		VoiceGatewayWSSURL: defaultVoiceGatewayWSSURL,
		SessionTicketTTL:   defaultSessionTicketTTL,
		InternalAPIToken:   "production-internal-token-long",
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected production secret error")
	}

	cfg.AuthJWTSecret = "production-jwt-secret-must-be-long-enough"
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected production MYSQL_DSN error")
	}

	cfg.MySQLDSN = "fw:fw@tcp(127.0.0.1:3306)/fluentwork"
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
	}
}

func TestValidateRejectsDevSecretOutsideDevelopment(t *testing.T) {
	cfg := Config{
		HTTPAddr:           ":8080",
		AppEnv:             "staging",
		AuthJWTSecret:      DevJWTSecret,
		AccessTokenTTL:     time.Hour,
		RefreshTokenTTL:    24 * time.Hour,
		MySQLDSN:           "fw:fw@tcp(127.0.0.1:3306)/fluentwork",
		VoiceGatewayWSSURL: defaultVoiceGatewayWSSURL,
		SessionTicketTTL:   defaultSessionTicketTTL,
		InternalAPIToken:   "staging-internal-token-long",
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected staging secret error")
	}
}

func TestLoadRejectsDevSecretDefaultOutsideDevelopment(t *testing.T) {
	t.Setenv("APP_ENV", "staging")
	t.Setenv("INTERNAL_API_TOKEN", "")
	t.Setenv("AUTH_JWT_SECRET", "")
	cfg := Load()
	if cfg.InternalAPIToken != "" {
		t.Fatalf("InternalAPIToken should not default outside development, got %q", cfg.InternalAPIToken)
	}
	if cfg.AuthJWTSecret != "" {
		t.Fatalf("AuthJWTSecret should not default outside development, got %q", cfg.AuthJWTSecret)
	}
	if err := cfg.Validate(); err == nil {
		t.Fatal("expected Validate to reject missing secrets outside development")
	}
}

func TestLoadArkPrefersExplicitAPIKey(t *testing.T) {
	t.Setenv("APP_ENV", "development")
	t.Setenv("ARK_API_KEY", "explicit-key")
	t.Setenv("ARK_API_KEY_DEV", "dev-key")
	t.Setenv("ARK_EP_REVIEW_REFINE", "ep-review")

	cfg := Load()
	if cfg.ArkAPIKey != "explicit-key" {
		t.Fatalf("ArkAPIKey = %q", cfg.ArkAPIKey)
	}
	if cfg.ArkReviewRefineEP != "ep-review" {
		t.Fatalf("ArkReviewRefineEP = %q", cfg.ArkReviewRefineEP)
	}
}
