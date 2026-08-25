package voiceproto_test

import (
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"testing"

	"github.com/FluentWork/fluentwork-backend/internal/voiceproto"
)

func TestControlFrameRoundTrip(t *testing.T) {
	t.Parallel()

	cases := []any{
		voiceproto.Auth{Type: voiceproto.TypeAuth, Ticket: "abc"},
		voiceproto.SessionReady{Type: voiceproto.TypeSessionReady, SessionID: "s1", UserID: "u1"},
		voiceproto.SessionStart{Type: voiceproto.TypeSessionStart, SceneType: "demo"},
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
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("runtime.Caller failed")
	}
	root := filepath.Clean(filepath.Join(filepath.Dir(file), "..", ".."))
	schemaPath := filepath.Join(root, "api", "wss-control-frames-v1.json")
	raw, err := os.ReadFile(schemaPath)
	if err != nil {
		t.Fatalf("read schema: %v", err)
	}
	var doc map[string]any
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("schema json: %v", err)
	}
	if doc["title"] == nil {
		t.Fatal("schema missing title")
	}
	defs, ok := doc["$defs"].(map[string]any)
	if !ok {
		t.Fatal("schema missing $defs")
	}
	for _, name := range []string{"auth", "sessionReady", "sessionStart", "sessionEnd", "interrupt", "error", "ping", "pong"} {
		if _, ok := defs[name]; !ok {
			t.Fatalf("schema missing $defs.%s", name)
		}
	}
}
