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
	for _, name := range []string{"auth", "sessionReady", "sessionStart", "aiTurnEnd", "sessionEnd", "interrupt", "error", "ping", "pong"} {
		if _, ok := defs[name]; !ok {
			t.Fatalf("schema missing $defs.%s", name)
		}
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
