package voicegateway

import (
	"log/slog"
	"strings"
)

// NewVoiceProvider selects the configured gateway voice provider.
//
// Selection priority (highest → lowest):
//  1. cfg.Provider == "volc-duplex"  → NewVolcDuplexProvider (live provider)
//  2. cfg.Provider == "mock" | ""    → MockVoiceProvider (local dev / tests)
//  3. anything else                  → MockVoiceProvider (safe fallback)
//
// In production the gateway expects cfg.Provider to come from the env-var
// VOICE_GATEWAY_PROVIDER. LoadConfig() auto-loads .env.volc.local so the
// provider can flip to volc-duplex without editing the committed .env.dev.
func NewVoiceProvider(cfg Config, logger *slog.Logger) VoiceProvider {
	switch strings.ToLower(strings.TrimSpace(cfg.Provider)) {
	case "volc-duplex":
		return NewVolcDuplexProvider(cfg, logger)
	case "", "mock":
		fallthrough
	default:
		return MockVoiceProvider{}
	}
}
