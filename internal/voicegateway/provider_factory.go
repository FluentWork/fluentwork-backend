package voicegateway

import (
	"log/slog"
	"strings"
)

// NewVoiceProvider selects the configured gateway voice provider.
//
// Selection priority (highest → lowest):
//  1. cfg.Provider == "volc-duplex"  → NewVolcDuplexProvider (live provider)
//  2. cfg.Provider == "dev-echo"    → NewDevEchoVoiceProvider (local B12 path)
//  3. cfg.Provider == "mock" | ""    → MockVoiceProvider (local dev / tests)
//  4. anything else                  → MockVoiceProvider (safe fallback)
//
// The "dev-echo" option exists so developers can exercise the full
// B14 → B12 → feedback.badge path without standing up Volcengine.
// Configure both:
//
//	VOICE_GATEWAY_PROVIDER=dev-echo
//	VOICE_DEV_ECHO_TEXT="let's ship it"
//
// and the dev-echo provider returns that text as ServerASRText on every
// user.speech.end — which the B12 hit detector scores against whatever
// phrase blocks exist in the corpus.
//
// In production the gateway expects cfg.Provider to come from the env-var
// VOICE_GATEWAY_PROVIDER. LoadConfig() auto-loads .env.volc.local so the
// provider can flip to volc-duplex without editing the committed .env.dev.
func NewVoiceProvider(cfg Config, logger *slog.Logger) VoiceProvider {
	switch strings.ToLower(strings.TrimSpace(cfg.Provider)) {
	case "volc-duplex":
		return NewVolcDuplexProvider(cfg, logger)
	case "dev-echo":
		return NewDevEchoVoiceProvider(cfg.DevEchoText, logger)
	case "", "mock":
		fallthrough
	default:
		return MockVoiceProvider{}
	}
}
