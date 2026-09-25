package voicegateway

import (
	"testing"
	"time"
)

// TestWireRescue_CarriesConfiguredTimings covers the half rescue_wiring_test.go
// cannot see. That test lives in the external package and proves WireRescue
// returns true; it cannot prove the feature was wired with the numbers the
// operator asked for. A Config field that is read but never threaded through
// looks exactly like a working one from the outside, so every knob is asserted
// here against the component that consumes it.
func TestWireRescue_CarriesConfiguredTimings(t *testing.T) {
	handler := NewHandler(nil, nil, nil, nil, Options{})
	cfg := Config{
		RescueEnabled:        true,
		AppServerInternalURL: "http://127.0.0.1:1",
		InternalAPIToken:     "dev-token-long-enough-to-pass",
		RescueLevel1After:    2 * time.Second,
		RescueLevel2After:    4 * time.Second,
		RescueLevel3After:    6 * time.Second,
		RescueGenTimeout:     1500 * time.Millisecond,
		RescueSynthTimeout:   700 * time.Millisecond,
	}
	if !WireRescue(handler, cfg, nil) {
		t.Fatal("WireRescue returned false for an enabled config")
	}

	got := handler.silenceDetector.Thresholds()
	if got.Level1 != cfg.RescueLevel1After || got.Level2 != cfg.RescueLevel2After || got.Level3 != cfg.RescueLevel3After {
		t.Fatalf("detector thresholds = %s / %s / %s, want %s / %s / %s",
			got.Level1, got.Level2, got.Level3,
			cfg.RescueLevel1After, cfg.RescueLevel2After, cfg.RescueLevel3After)
	}

	gen, ok := handler.rescueOrchestrator.rescueGen.(*HTTPRescueGenerator)
	if !ok {
		t.Fatalf("generator is %T, want *HTTPRescueGenerator", handler.rescueOrchestrator.rescueGen)
	}
	if gen.Client == nil {
		t.Fatal("generator has no http.Client, so it carries no timeout")
	}
	if gen.Client.Timeout != cfg.RescueGenTimeout {
		t.Fatalf("generator timeout = %s, want %s", gen.Client.Timeout, cfg.RescueGenTimeout)
	}

	synth, ok := handler.rescueOrchestrator.synth.(*HTTPRescueSynthesizer)
	if !ok {
		t.Fatalf("synthesizer is %T, want *HTTPRescueSynthesizer", handler.rescueOrchestrator.synth)
	}
	if synth.timeout != cfg.RescueSynthTimeout {
		t.Fatalf("synthesizer timeout = %s, want %s", synth.timeout, cfg.RescueSynthTimeout)
	}

	// The generation budget is the first rung's spacing, not a fixed 3s: shrink
	// the rungs without shrinking this and a rung outlives the next one's due
	// time. See RescueOrchestrator.generateText.
	if handler.rescueOrchestrator.rungBudget != cfg.RescueLevel1After {
		t.Fatalf("rung budget = %s, want %s (the first rung's spacing)",
			handler.rescueOrchestrator.rungBudget, cfg.RescueLevel1After)
	}
}

// The other half of the contract: a Config that never set the timings must wire
// the compiled-in defaults rather than zero-valued ones that happen to work.
// Every component resolves its own zero, and this pins that they do.
func TestWireRescue_ZeroTimingsFallBackToDefaults(t *testing.T) {
	handler := NewHandler(nil, nil, nil, nil, Options{})
	cfg := Config{
		RescueEnabled:        true,
		AppServerInternalURL: "http://127.0.0.1:1",
	}
	if !WireRescue(handler, cfg, nil) {
		t.Fatal("WireRescue returned false for an enabled config")
	}

	got := handler.silenceDetector.Thresholds()
	want := RescueThresholds{
		Level1: DefaultRescueLevel1After,
		Level2: DefaultRescueLevel2After,
		Level3: DefaultRescueLevel3After,
	}
	if got != want {
		t.Fatalf("detector thresholds = %+v, want the compiled-in defaults %+v", got, want)
	}

	gen := handler.rescueOrchestrator.rescueGen.(*HTTPRescueGenerator)
	if gen.Client.Timeout != DefaultRescueGenTimeout {
		t.Fatalf("generator timeout = %s, want %s", gen.Client.Timeout, DefaultRescueGenTimeout)
	}
	synth := handler.rescueOrchestrator.synth.(*HTTPRescueSynthesizer)
	if synth.timeout != DefaultRescueSynthTimeout {
		t.Fatalf("synthesizer timeout = %s, want %s", synth.timeout, DefaultRescueSynthTimeout)
	}
	if handler.rescueOrchestrator.rungBudget != DefaultRescueLevel1After {
		t.Fatalf("rung budget = %s, want %s", handler.rescueOrchestrator.rungBudget, DefaultRescueLevel1After)
	}
}
