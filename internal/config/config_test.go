package config

import (
	"testing"
	"time"
)

func TestLoadDefaults(t *testing.T) {
	t.Setenv("HTTP_ADDR", "")
	t.Setenv("APP_ENV", "")
	t.Setenv("MYSQL_DSN", "")
	t.Setenv("AUTH_JWT_SECRET", "")
	t.Setenv("AUTH_ACCESS_TTL", "")
	t.Setenv("AUTH_REFRESH_TTL", "")
	t.Setenv("VOICE_GATEWAY_WSS_URL", "")
	t.Setenv("SESSION_TICKET_TTL", "")
	t.Setenv("INTERNAL_API_TOKEN", "")

	cfg := Load()
	if cfg.HTTPAddr != defaultHTTPAddr {
		t.Fatalf("HTTPAddr = %q", cfg.HTTPAddr)
	}
	if cfg.AppEnv != defaultAppEnv {
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
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() = %v", err)
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

func TestDurationOrFallsBackOnInvalidValue(t *testing.T) {
	t.Setenv("AUTH_ACCESS_TTL", "not-a-duration")
	if got := durationOr("AUTH_ACCESS_TTL", time.Minute); got != time.Minute {
		t.Fatalf("durationOr() = %s", got)
	}
}
