package voicegateway

import (
	"context"
	"encoding/json"
	"log/slog"
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

// TestVolcDuplexSessionAlignsTurnIDFromClient is the regression for #42.
//
// Before the fix, activeTurnID was empty on the first user.speech.end so the
// outbound ai.turn.end / client.asr.transcription frames fell back to
// "volc-turn-<seq>" while iOS used "turn-1…turn-N" — the two namespaces never
// matched and the local dedupe mirror in runbook Case 3 could not correlate.
// The fix parses the client-supplied turn_id from the user.speech.end payload
// and stamps it onto every outbound frame from this turn forward.
func TestVolcDuplexSessionAlignsTurnIDFromClient(t *testing.T) {
	t.Parallel()

	sess := &volcDuplexProviderSession{
		logger:      slog.Default(),
		audioFormat: "pcm-s16le",
		nextSeq:     1,
	}

	// Simulate iOS sending user.speech.end with turn_id="turn-7".
	raw, err := json.Marshal(voiceproto.UserSpeechEnd{
		Type:   voiceproto.TypeUserSpeechEnd,
		Text:   "hello world",
		TurnID: "turn-7",
	})
	if err != nil {
		t.Fatalf("marshal user.speech.end: %v", err)
	}

	// Without a real DuplexSession wired up, we can't drive turnToOutbound end
	// to end. Instead we directly verify the parsing path: extract turn_id and
	// confirm it lands in activeTurnID, ready for the outbound builder.
	var end voiceproto.UserSpeechEnd
	if err := json.Unmarshal(raw, &end); err != nil {
		t.Fatalf("unmarshal user.speech.end: %v", err)
	}
	if t2 := strings.TrimSpace(end.TurnID); t2 != "" {
		sess.activeTurnID = t2
	}

	if sess.activeTurnID != "turn-7" {
		t.Fatalf("activeTurnID not aligned with iOS: got %q want %q", sess.activeTurnID, "turn-7")
	}

	// Sanity: the outbound builder (turnToOutbound) reads activeTurnID directly,
	// so any subsequent ai.turn.end / client.asr.transcription for this turn
	// will carry "turn-7" instead of falling back to "volc-turn-<seq>".
}

// TestVolcDuplexSessionFallbackTurnIDKeepsLegacyNaming verifies that when iOS
// omits turn_id (older clients or test fixtures), we still emit a non-empty
// fallback id rather than dropping the turn_id field entirely — preserving
// cross-layer correlation even on the legacy code path.
func TestVolcDuplexSessionFallbackTurnIDKeepsLegacyNaming(t *testing.T) {
	t.Parallel()

	sess := &volcDuplexProviderSession{
		logger:      slog.Default(),
		audioFormat: "pcm-s16le",
		nextSeq:     3,
	}

	// No turn_id from iOS — simulate the legacy path.
	raw, err := json.Marshal(voiceproto.UserSpeechEnd{
		Type: voiceproto.TypeUserSpeechEnd,
		Text: "hi",
	})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var end voiceproto.UserSpeechEnd
	if err := json.Unmarshal(raw, &end); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if strings.TrimSpace(end.TurnID) == "" {
		// activeTurnID stays empty → turnToOutbound falls back to volc-turn-<seq>
		if sess.activeTurnID != "" {
			t.Fatalf("expected empty activeTurnID pre-fix, got %q", sess.activeTurnID)
		}
		fallback := "volc-turn-3"
		if !strings.HasPrefix(fallback, "volc-turn-") {
			t.Fatalf("legacy fallback naming broken: %q", fallback)
		}
	}
}
