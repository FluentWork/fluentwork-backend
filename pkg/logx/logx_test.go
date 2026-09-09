package logx

import (
	"bytes"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"testing"
	"time"
)

func TestSegmentEndEmitsTraceShape(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	seg := Begin(logger, "voice.duplex.collect_turn",
		"module", "voicepoc.duplex",
		"session_id", "s1",
		"turn_id", "turn-1",
		"log_id", "volc-abc",
	)
	time.Sleep(time.Millisecond)
	seg.End(nil, "outcome", "partial")

	done := lastJSONLine(t, buf.Bytes())
	if done["msg"] != "voice.duplex.collect_turn.done" {
		t.Fatalf("msg = %#v", done["msg"])
	}
	if done["session_id"] != "s1" || done["turn_id"] != "turn-1" || done["log_id"] != "volc-abc" {
		t.Fatalf("trace ids missing: %#v", done)
	}
	if done["outcome"] != "partial" {
		t.Fatalf("explicit outcome must win, got %#v", done["outcome"])
	}
	if _, ok := done["duration_ms"]; !ok {
		t.Fatal("missing duration_ms")
	}
	if _, ok := done["ts"]; !ok {
		t.Fatal("missing ts")
	}
}

func TestSegmentEndDefaultsOutcomeFromErr(t *testing.T) {
	var buf bytes.Buffer
	logger := slog.New(slog.NewJSONHandler(&buf, nil))
	seg := Begin(logger, "voice.handshake")
	seg.End(errors.New("boom"))
	done := lastJSONLine(t, buf.Bytes())
	if done["outcome"] != "error" {
		t.Fatalf("outcome = %#v", done["outcome"])
	}
}

func lastJSONLine(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	lines := bytes.Split(bytes.TrimSpace(raw), []byte("\n"))
	if len(lines) == 0 {
		t.Fatal("no log lines")
	}
	var doc map[string]any
	if err := json.Unmarshal(lines[len(lines)-1], &doc); err != nil {
		t.Fatalf("json: %v (%s)", err, strings.TrimSpace(string(lines[len(lines)-1])))
	}
	return doc
}
