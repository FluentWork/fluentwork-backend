package voicegateway_test

import (
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
