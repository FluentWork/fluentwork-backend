package voicegateway

import (
	"log/slog"
	"strings"
)

// NewVoiceProvider selects the configured gateway voice provider.
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
