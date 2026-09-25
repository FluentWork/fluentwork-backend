package voicegateway_test

import (
	"strings"
	"testing"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/voicegateway"
)

// rescueConfigEnvKeys are the variables LoadConfig reads that this checkout's
// .env.volc.local could otherwise supply. t.Setenv marks a key as *set*
// (empty), which is what stops loadDotenvIfPresent from injecting the real
// values — the same trick, and the same reason, as the loop in
// TestLoadConfigPicksVolcSpeechDefaults.
var rescueConfigEnvKeys = []string{
	"VOICE_GATEWAY_PROVIDER",
	"VOICE_GATEWAY_CLIENT_AUDIO_FORMAT",
	"VOICE_GATEWAY_VOLC_SPEECH_API_KEY",
	"VOLC_POC_API_KEY",
	"VOLC_SPEECH_API_KEY",
	"VOLC_SPEECH_API_KEY_DEV",
	"VOLC_POC_ENDPOINT",
	"VOLC_DUPLEX_MODEL",
	"VOLC_DUPLEX_VOICE",
	"VOICE_RESCUE_LEVEL1_AFTER",
	"VOICE_RESCUE_LEVEL2_AFTER",
	"VOICE_RESCUE_LEVEL3_AFTER",
	"VOICE_RESCUE_GEN_TIMEOUT",
	"VOICE_RESCUE_SYNTH_TIMEOUT",
}

func neutralizeConfigEnv(t *testing.T) {
	t.Helper()
	for _, key := range rescueConfigEnvKeys {
		t.Setenv(key, "")
	}
}

// loadDevConfig loads a Config that is expected to validate, with the ladder
// timings neutralized so the test owns every value it asserts on.
func loadDevConfig(t *testing.T) voicegateway.Config {
	t.Helper()
	neutralizeConfigEnv(t)
	t.Setenv("APP_ENV", "development")
	return voicegateway.LoadConfig()
}

// The point of the knobs: a value in the environment reaches the Config.
func TestLoadConfigRescueTimingsAreConfigurable(t *testing.T) {
	neutralizeConfigEnv(t)
	t.Setenv("APP_ENV", "development")
	t.Setenv("VOICE_RESCUE_LEVEL1_AFTER", "2s")
	t.Setenv("VOICE_RESCUE_LEVEL2_AFTER", "4s")
	t.Setenv("VOICE_RESCUE_LEVEL3_AFTER", "6s")
	t.Setenv("VOICE_RESCUE_GEN_TIMEOUT", "1500ms")
	t.Setenv("VOICE_RESCUE_SYNTH_TIMEOUT", "700ms")

	cfg := voicegateway.LoadConfig()

	if cfg.RescueLevel1After != 2*time.Second ||
		cfg.RescueLevel2After != 4*time.Second ||
		cfg.RescueLevel3After != 6*time.Second {
		t.Fatalf("rung spacing = %s / %s / %s, want 2s / 4s / 6s",
			cfg.RescueLevel1After, cfg.RescueLevel2After, cfg.RescueLevel3After)
	}
	if cfg.RescueGenTimeout != 1500*time.Millisecond {
		t.Fatalf("RescueGenTimeout = %s, want 1.5s", cfg.RescueGenTimeout)
	}
	if cfg.RescueSynthTimeout != 700*time.Millisecond {
		t.Fatalf("RescueSynthTimeout = %s, want 700ms", cfg.RescueSynthTimeout)
	}
	// A coherent ladder must survive its own validation: 1.5s + 0.7s fit inside
	// the 2s first rung.
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() on a coherent ladder = %v", err)
	}
}

// A value that is set but unparseable must fail startup naming the variable.
// Falling back to the default would leave the operator believing a number that
// is not running — VOICE_RESCUE_LEVEL1_AFTER=5 means five seconds to everyone
// who writes it and three to time.ParseDuration.
func TestLoadConfigRescueTimingRejectsMalformedValue(t *testing.T) {
	cases := []struct{ key, value string }{
		{"VOICE_RESCUE_LEVEL1_AFTER", "5"},    // no unit
		{"VOICE_RESCUE_LEVEL2_AFTER", "soon"}, // not a duration at all
		{"VOICE_RESCUE_GEN_TIMEOUT", "2500"},  // the old literal, unit forgotten
		{"VOICE_RESCUE_LEVEL3_AFTER", "0s"},   // set to zero on purpose
		{"VOICE_RESCUE_SYNTH_TIMEOUT", "-1s"}, // negative
	}
	for _, tc := range cases {
		t.Run(tc.key+"="+tc.value, func(t *testing.T) {
			neutralizeConfigEnv(t)
			t.Setenv("APP_ENV", "development")
			t.Setenv(tc.key, tc.value)

			err := voicegateway.LoadConfig().Validate()
			if err == nil {
				t.Fatalf("%s=%q was accepted; it must fail startup", tc.key, tc.value)
			}
			if !strings.Contains(err.Error(), tc.key) {
				t.Fatalf("error %q does not name %s", err, tc.key)
			}
			if !strings.Contains(err.Error(), tc.value) {
				t.Fatalf("error %q does not quote the offending value %q", err, tc.value)
			}
		})
	}
}

// Each case is a relationship the rescue code relies on. Making the values
// configurable must not become a way around them.
func TestValidateRescueTimingsRefuseBrokenLadders(t *testing.T) {
	cases := []struct {
		name    string
		mutate  func(*voicegateway.Config)
		wantKey string
	}{
		{
			name:    "rung 2 before rung 1",
			mutate:  func(c *voicegateway.Config) { c.RescueLevel2After = c.RescueLevel1After - time.Second },
			wantKey: "VOICE_RESCUE_LEVEL2_AFTER",
		},
		{
			name:    "rung 3 before rung 2",
			mutate:  func(c *voicegateway.Config) { c.RescueLevel3After = c.RescueLevel2After - time.Second },
			wantKey: "VOICE_RESCUE_LEVEL3_AFTER",
		},
		{
			name:    "synthesis outlives the rung it speaks",
			mutate:  func(c *voicegateway.Config) { c.RescueSynthTimeout = c.RescueLevel1After + time.Second },
			wantKey: "VOICE_RESCUE_SYNTH_TIMEOUT",
		},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			cfg := loadDevConfig(t)
			tc.mutate(&cfg)

			err := cfg.Validate()
			if err == nil {
				t.Fatalf("Validate() accepted a broken ladder")
			}
			if !strings.Contains(err.Error(), tc.wantKey) {
				t.Fatalf("error %q does not name %s", err, tc.wantKey)
			}
		})
	}
}

// Shortening the rung spacing on its own must be allowed: it is the whole
// reason the knobs exist ("救援计划时间太长了" is a spacing complaint), and the
// first version of the rules refused it — 2s rungs collided with the default
// 2500ms generator timeout, which is not an effective bound at all, because
// generateText already caps generation at the rung budget through the context.
//
// The synthesizer is the asymmetric half and is covered above: its timeout is
// the effective bound, so it does have to fit.
func TestValidateRescueTimingsAllowShorteningRungsAlone(t *testing.T) {
	neutralizeConfigEnv(t)
	t.Setenv("APP_ENV", "development")
	t.Setenv("VOICE_RESCUE_LEVEL1_AFTER", "2s")
	t.Setenv("VOICE_RESCUE_LEVEL2_AFTER", "4s")
	t.Setenv("VOICE_RESCUE_LEVEL3_AFTER", "6s")
	// VOICE_RESCUE_GEN_TIMEOUT deliberately left at its 2500ms default, which
	// now exceeds the 2s spacing.

	cfg := voicegateway.LoadConfig()
	if cfg.RescueGenTimeout <= cfg.RescueLevel1After {
		t.Fatalf("precondition: generator timeout %s should exceed the %s spacing for this test to mean anything",
			cfg.RescueGenTimeout, cfg.RescueLevel1After)
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() refused a shortened ladder: %v", err)
	}
}

// A Config assembled by hand leaves the timings zero. The components resolve
// those to the compiled-in defaults, so Validate has to judge what will
// actually run rather than reject a zero nobody wrote — this is the shape
// TestConfigAcceptsVolcProviderWithSpeechKey uses, and it broke once already
// when this rule was written the other way round.
func TestValidateRescueTimingsTolerateHandBuiltConfig(t *testing.T) {
	cfg := voicegateway.Config{
		HTTPAddr:             ":8081",
		AppEnv:               "development",
		AppServerInternalURL: "http://127.0.0.1:8080",
		InternalAPIToken:     "dev-token-long-enough-to-pass",
		Provider:             "mock",
		ClientAudioFormat:    "opus-framed",
		IdleTimeout:          2 * time.Minute,
	}
	if err := cfg.Validate(); err != nil {
		t.Fatalf("Validate() on a hand-built Config = %v", err)
	}
}
