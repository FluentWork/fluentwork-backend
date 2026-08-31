package voiceproto_test

import (
	"encoding/json"
	"testing"

	"github.com/FluentWork/fluentwork-backend/internal/voiceproto"
	sharedschemas "github.com/FluentWork/fluentwork-backend/schemas"
)

func TestControlFrameRoundTrip(t *testing.T) {
	t.Parallel()

	cases := []any{
		voiceproto.Auth{Type: voiceproto.TypeAuth, Ticket: "abc"},
		voiceproto.SessionReady{Type: voiceproto.TypeSessionReady, SessionID: "s1", UserID: "u1"},
		voiceproto.SessionStart{Type: voiceproto.TypeSessionStart, SceneType: "demo"},
		voiceproto.AITurnEnd{Type: voiceproto.TypeAITurnEnd, TurnID: "turn-1"},
		voiceproto.Interrupt{Type: voiceproto.TypeInterrupt},
		voiceproto.SessionEnd{Type: voiceproto.TypeSessionEnd, Reason: "user"},
		voiceproto.ErrorFrame{Type: voiceproto.TypeError, Code: "unauthenticated", Message: "bad ticket"},
		voiceproto.Ping{Type: voiceproto.TypePing, TS: 1},
		voiceproto.Pong{Type: voiceproto.TypePong, TS: 1},
		voiceproto.FeedbackBadge{
			Type:          voiceproto.TypeFeedbackBadge,
			Badge:         "fluency+1",
			PhraseBlockID: "block-1",
			Tier:          voiceproto.BadgeTierSoft,
			SessionID:     "s1",
			TurnID:        "turn-1",
			DedupeKey:     "s1|turn-1|block-1",
		},
	}
	for _, c := range cases {
		raw, err := json.Marshal(c)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		typ, err := voiceproto.DecodeType(raw)
		if err != nil {
			t.Fatalf("DecodeType(%s): %v", raw, err)
		}
		if typ == "" {
			t.Fatalf("empty type for %s", raw)
		}
	}
}

func TestSchemaFilePresent(t *testing.T) {
	t.Parallel()
	var doc map[string]any
	if err := json.Unmarshal(sharedschemas.WSSControlFramesV1, &doc); err != nil {
		t.Fatalf("schema json: %v", err)
	}
	if doc["title"] == nil {
		t.Fatal("schema missing title")
	}
	defs, ok := doc["$defs"].(map[string]any)
	if !ok {
		t.Fatal("schema missing $defs")
	}
	for _, name := range []string{"auth", "sessionReady", "sessionStart", "aiTurnEnd", "sessionEnd", "interrupt", "error", "ping", "pong", "feedbackBadge"} {
		if _, ok := defs[name]; !ok {
			t.Fatalf("schema missing $defs.%s", name)
		}
	}
}

func TestFeedbackBadgeDedupeKey(t *testing.T) {
	t.Parallel()
	got := voiceproto.ComposeBadgeDedupeKey("s1", "turn-1", "block-1")
	want := "s1|turn-1|block-1"
	if got != want {
		t.Fatalf("ComposeBadgeDedupeKey: got %q want %q", got, want)
	}
	// Missing fields produce empty key (caller must skip emission).
	if key := voiceproto.ComposeBadgeDedupeKey("", "turn-1", "block-1"); key != "" {
		t.Fatalf("missing session should yield empty key, got %q", key)
	}
	if key := voiceproto.ComposeBadgeDedupeKey("s1", "", "block-1"); key != "" {
		t.Fatalf("missing turn should yield empty key, got %q", key)
	}
	if key := voiceproto.ComposeBadgeDedupeKey("s1", "turn-1", " "); key != "" {
		t.Fatalf("blank phrase_block should yield empty key, got %q", key)
	}
}

func TestNewFeedbackBadgePopulatesFields(t *testing.T) {
	t.Parallel()
	badge := voiceproto.NewFeedbackBadge(
		"fluency+1", "block-1", voiceproto.BadgeTierSoft, "s1", "turn-1",
	)
	if badge.Type != voiceproto.TypeFeedbackBadge {
		t.Fatalf("Type: got %q want %q", badge.Type, voiceproto.TypeFeedbackBadge)
	}
	if badge.Badge != "fluency+1" {
		t.Fatalf("Badge: got %q want %q", badge.Badge, "fluency+1")
	}
	if badge.DedupeKey != "s1|turn-1|block-1" {
		t.Fatalf("DedupeKey: got %q", badge.DedupeKey)
	}
}

func TestFeedbackBadgeFrameDecode(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"type":"feedback.badge","badge":"fluency+1","phrase_block_id":"block-1","tier":"soft","session_id":"s1","turn_id":"turn-1","dedupe_key":"s1|turn-1|block-1"}`)
	var badge voiceproto.FeedbackBadge
	if err := json.Unmarshal(raw, &badge); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if badge.Badge != "fluency+1" || badge.PhraseBlockID != "block-1" {
		t.Fatalf("unexpected decode: %+v", badge)
	}
	if typ, err := voiceproto.DecodeType(raw); err != nil || typ != voiceproto.TypeFeedbackBadge {
		t.Fatalf("DecodeType: got %q err %v", typ, err)
	}
}

func TestSpeechObservabilityEventSchemaMirrorPresent(t *testing.T) {
	t.Parallel()

	var doc map[string]any
	if err := json.Unmarshal(sharedschemas.SpeechObservabilityEventsV1, &doc); err != nil {
		t.Fatalf("schema json: %v", err)
	}
	if doc["title"] == nil {
		t.Fatal("schema missing title")
	}
	defs, ok := doc["$defs"].(map[string]any)
	if !ok {
		t.Fatal("schema missing $defs")
	}
	for _, name := range []string{
		"eventBase",
		"speechSessionStarted",
		"speechSessionFailed",
		"speechTurnEnded",
		"speechTransportDisconnected",
	} {
		if _, ok := defs[name]; !ok {
			t.Fatalf("schema missing $defs.%s", name)
		}
	}
}
