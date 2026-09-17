package voicegateway

import "log/slog"

// WireRescue attaches B8 stuck rescue to a handler when the deployment enables
// it, and reports whether it did.
//
// This lives here, as a function, because the mechanism stayed dark for months
// while every one of its components was well tested: nothing tested the call
// that turns it on. A wiring decision that only exists inside main() cannot be
// regression-tested; one that exists here can (see rescue_wiring_test.go).
func WireRescue(handler *Handler, cfg Config, logger *slog.Logger) bool {
	if handler == nil || !cfg.RescueEnabled {
		return false
	}
	if logger == nil {
		logger = slog.Default()
	}
	handler.SetRescueComponents(
		NewSilenceDetector(),
		NewRescueOrchestrator(
			// App-server generates the ladder (方案 B): one model path, one
			// prompt home, one cost ledger. Any failure falls back to the
			// reviewed static library inside the orchestrator.
			NewHTTPRescueGenerator(cfg.AppServerInternalURL, cfg.InternalAPIToken, logger),
			nil, // synthesizer: TTS is built but not turned on (B17/P2-4)
			logger,
		),
	)
	return true
}

// RescueEnabled reports whether rescue components are attached. Exported for
// the wiring test and for a startup log that does not have to guess.
func (h *Handler) RescueEnabled() bool {
	if h == nil {
		return false
	}
	return h.silenceDetector != nil && h.rescueOrchestrator != nil
}
