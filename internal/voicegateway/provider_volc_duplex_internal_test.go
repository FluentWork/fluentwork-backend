package voicegateway

import (
	"context"
	"strings"
	"testing"

	"github.com/FluentWork/fluentwork-backend/internal/voiceproto"
)

func TestVolcDuplexProviderOpenRequiresSpeechKey(t *testing.T) {
	t.Parallel()

	provider := NewVolcDuplexProvider(Config{
		Provider:          "volc-duplex",
		ClientAudioFormat: "pcm-s16le",
	}, nil)
	_, err := provider.Open(context.Background(), ConsumedTicket{})
	if err == nil || !strings.Contains(err.Error(), "missing speech API key") {
		t.Fatalf("expected missing key error, got %v", err)
	}
}

func TestInstructionsForSessionStartIncludesContext(t *testing.T) {
	t.Parallel()

	text := instructionsForSessionStart(voiceproto.SessionStart{
		Type:       voiceproto.TypeSessionStart,
		SceneType:  "daily-read",
		MaterialID: "m-42",
	})
	if !strings.Contains(text, "daily-read") || !strings.Contains(text, "m-42") {
		t.Fatalf("unexpected instructions: %q", text)
	}
}
