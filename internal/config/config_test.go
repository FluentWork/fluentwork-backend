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

// E3: the scheduling ladder is the environment's to set, and an unset variable
// must land on the PRD default rather than on zero.
func TestLoadDrillScheduleDefaults(t *testing.T) {
	for _, key := range []string{
		"DRILL_PROMOTE_STREAK", "DRILL_TRAINING_INTERVAL", "DRILL_AUTOMATED_INTERVAL",
		"DRILL_AUTOMATED_REVIEW_INTERVAL", "DRILL_FAIL_INTERVAL", "DRILL_ROUND_SIZE",
		"DRILL_DAILY_NEW_BLOCK_LIMIT",
	} {
		t.Setenv(key, "")
	}
	cfg := Load()
	if cfg.DrillPromoteStreak != defaultDrillPromoteStreak ||
		cfg.DrillTrainingInterval != defaultDrillTrainingInterval ||
		cfg.DrillAutomatedInterval != defaultDrillAutomatedInterval ||
		cfg.DrillAutomatedReviewInterval != defaultDrillAutomatedReviewInterval ||
		cfg.DrillFailInterval != defaultDrillFailInterval ||
		cfg.DrillRoundSize != defaultDrillRoundSize ||
		cfg.DrillDailyNewBlockLimit != defaultDrillDailyNewBlockLimit {
		t.Fatalf("drill defaults not applied: %+v", cfg)
	}
	if err := cfg.validateDrillSchedule(); err != nil {
		t.Fatalf("defaults must validate: %v", err)
	}
}

func TestLoadDrillScheduleOverrides(t *testing.T) {
	t.Setenv("DRILL_PROMOTE_STREAK", "2")
	t.Setenv("DRILL_TRAINING_INTERVAL", "6h")
	t.Setenv("DRILL_AUTOMATED_INTERVAL", "72h")
	t.Setenv("DRILL_AUTOMATED_REVIEW_INTERVAL", "720h")
	t.Setenv("DRILL_FAIL_INTERVAL", "15m")
	t.Setenv("DRILL_ROUND_SIZE", "6")
	t.Setenv("DRILL_DAILY_NEW_BLOCK_LIMIT", "5")

	cfg := Load()
	if cfg.DrillPromoteStreak != 2 || cfg.DrillTrainingInterval != 6*time.Hour ||
		cfg.DrillAutomatedInterval != 72*time.Hour ||
		cfg.DrillAutomatedReviewInterval != 720*time.Hour ||
		cfg.DrillFailInterval != 15*time.Minute || cfg.DrillRoundSize != 6 ||
		cfg.DrillDailyNewBlockLimit != 5 {
		t.Fatalf("overrides not applied: %+v", cfg)
	}
	if err := cfg.validateDrillSchedule(); err != nil {
		t.Fatalf("Validate: %v", err)
	}
}

// A negative value is a typo, not a default: it must fail loudly rather than
// quietly disable the spacing the mechanism rests on.
func TestValidateDrillScheduleRejectsNegatives(t *testing.T) {
	cases := map[string]Config{
		"promote streak":    {DrillPromoteStreak: -1},
		"training interval": {DrillTrainingInterval: -time.Hour},
		"automated":         {DrillAutomatedInterval: -time.Hour},
		"automated review":  {DrillAutomatedReviewInterval: -time.Hour},
		"fail interval":     {DrillFailInterval: -time.Minute},
		"round size":        {DrillRoundSize: -1},
		"daily new blocks":  {DrillDailyNewBlockLimit: -1},
	}
	for name, cfg := range cases {
		t.Run(name, func(t *testing.T) {
			if err := cfg.validateDrillSchedule(); err == nil {
				t.Fatalf("%+v must not validate", cfg)
			}
		})
	}
	// Unparseable input keeps the default instead of failing the process.
	t.Setenv("DRILL_PROMOTE_STREAK", "three")
	if got := intOr("DRILL_PROMOTE_STREAK", defaultDrillPromoteStreak); got != defaultDrillPromoteStreak {
		t.Fatalf("unparseable value = %d, want the default", got)
	}
}

// 83_ §2.1's overdue window: default three days, and 0 is the documented way to
// switch the sweep off (so the parse must not turn 0 back into the default).
func TestLoadDrillOverdueWindow(t *testing.T) {
	t.Setenv("DRILL_OVERDUE_WINDOW", "")
	if got := Load().DrillOverdueWindow; got != defaultDrillOverdueWindow {
		t.Fatalf("default = %s, want %s", got, defaultDrillOverdueWindow)
	}
	t.Setenv("DRILL_OVERDUE_WINDOW", "12h")
	if got := Load().DrillOverdueWindow; got != 12*time.Hour {
		t.Fatalf("override = %s", got)
	}
	t.Setenv("DRILL_OVERDUE_WINDOW", "0")
	if got := Load().DrillOverdueWindow; got != 0 {
		t.Fatalf("zero must disable the sweep, got %s", got)
	}
	t.Setenv("DRILL_OVERDUE_WINDOW", "-1h")
	cfg := Load()
	if err := cfg.validateDrillSchedule(); err == nil {
		t.Fatal("a negative window must not validate")
	}
}
