package voicegateway_test

import (
	"os"
	"path/filepath"
	"testing"

	"github.com/FluentWork/fluentwork-backend/internal/voicegateway"
)

func TestNewVoiceProviderSelectsMockByDefault(t *testing.T) {
	t.Parallel()

	provider := voicegateway.NewVoiceProvider(voicegateway.Config{}, nil)
	if _, ok := provider.(voicegateway.MockVoiceProvider); !ok {
		t.Fatalf("expected MockVoiceProvider, got %T", provider)
	}
}

func TestNewVoiceProviderSelectsMockWhenExplicitlyMock(t *testing.T) {
	t.Parallel()

	provider := voicegateway.NewVoiceProvider(voicegateway.Config{
		Provider: "mock",
	}, nil)
	if _, ok := provider.(voicegateway.MockVoiceProvider); !ok {
		t.Fatalf("expected MockVoiceProvider, got %T", provider)
	}
}

func TestNewVoiceProviderSelectsVolcDuplex(t *testing.T) {
	t.Parallel()

	provider := voicegateway.NewVoiceProvider(voicegateway.Config{
		Provider:          "volc-duplex",
		ClientAudioFormat: "pcm-s16le",
		VolcSpeechAPIKey:  "api-key-123",
	}, nil)
	if _, ok := provider.(voicegateway.VolcDuplexProvider); !ok {
		t.Fatalf("expected VolcDuplexProvider, got %T", provider)
	}
}

// TestNewVoiceProviderFallsBackToMockForUnknown covers the safe-fallback
// contract: an unknown provider string MUST NOT crash and MUST return the
// mock so local dev stays runnable when someone typos the env var.
func TestNewVoiceProviderFallsBackToMockForUnknown(t *testing.T) {
	t.Parallel()

	provider := voicegateway.NewVoiceProvider(voicegateway.Config{
		Provider: "definitely-not-real",
	}, nil)
	if _, ok := provider.(voicegateway.MockVoiceProvider); !ok {
		t.Fatalf("expected MockVoiceProvider fallback, got %T", provider)
	}
}

func TestNewVoiceProviderSelectsDevEcho(t *testing.T) {
	t.Parallel()

	provider := voicegateway.NewVoiceProvider(voicegateway.Config{
		Provider:       "dev-echo",
		DevEchoText:    "let's ship it",
		DevEchoTTSMock: true,
	}, nil)
	echo, ok := provider.(voicegateway.DevEchoVoiceProvider)
	if !ok {
		t.Fatalf("expected DevEchoVoiceProvider, got %T", provider)
	}
	if echo.EchoText != "let's ship it" {
		t.Fatalf("EchoText = %q", echo.EchoText)
	}
	if !echo.TTSMock {
		t.Fatal("expected TTSMock to be copied from config")
	}
}

func TestNewVoiceProviderLoadsDevEchoFixtureFromPath(t *testing.T) {
	t.Parallel()

	pcm := voicegateway.DevEchoFixtureGenerator(20)
	path := filepath.Join(t.TempDir(), "sine.pcm")
	if err := os.WriteFile(path, pcm, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	provider := voicegateway.NewVoiceProvider(voicegateway.Config{
		Provider:           "dev-echo",
		DevEchoText:        "let's ship it",
		DevEchoFixturePath: path,
	}, nil)
	echo, ok := provider.(voicegateway.DevEchoVoiceProvider)
	if !ok {
		t.Fatalf("expected DevEchoVoiceProvider, got %T", provider)
	}
	if echo.FixturePath != path {
		t.Fatalf("FixturePath = %q", echo.FixturePath)
	}
	if len(echo.Fixture) != len(pcm) {
		t.Fatalf("Fixture len = %d, want %d", len(echo.Fixture), len(pcm))
	}
}

func TestNewVoiceProviderMissingFixtureDoesNotFail(t *testing.T) {
	t.Parallel()

	provider := voicegateway.NewVoiceProvider(voicegateway.Config{
		Provider:           "dev-echo",
		DevEchoFixturePath: filepath.Join(t.TempDir(), "missing.pcm"),
	}, nil)
	echo, ok := provider.(voicegateway.DevEchoVoiceProvider)
	if !ok {
		t.Fatalf("expected DevEchoVoiceProvider, got %T", provider)
	}
	if len(echo.Fixture) != 0 {
		t.Fatalf("missing fixture must not populate Fixture, got %d bytes", len(echo.Fixture))
	}
}

func TestNewVoiceProviderSkipsFixtureWhenTTSMock(t *testing.T) {
	t.Parallel()

	pcm := voicegateway.DevEchoFixtureGenerator(20)
	path := filepath.Join(t.TempDir(), "sine.pcm")
	if err := os.WriteFile(path, pcm, 0o644); err != nil {
		t.Fatalf("write fixture: %v", err)
	}

	provider := voicegateway.NewVoiceProvider(voicegateway.Config{
		Provider:           "dev-echo",
		DevEchoTTSMock:     true,
		DevEchoFixturePath: path,
	}, nil)
	echo, ok := provider.(voicegateway.DevEchoVoiceProvider)
	if !ok {
		t.Fatalf("expected DevEchoVoiceProvider, got %T", provider)
	}
	if len(echo.Fixture) != 0 {
		t.Fatalf("TTS mock must skip PCM fixture, got %d bytes", len(echo.Fixture))
	}
}
