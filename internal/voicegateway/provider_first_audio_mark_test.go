package voicegateway

import (
	"bytes"
	"encoding/json"
	"log/slog"
	"strings"
	"testing"
	"time"
)

// captureLogs points the session's logger at an in-memory buffer.
func captureLogs(buf *bytes.Buffer) *slog.Logger {
	return slog.New(slog.NewJSONHandler(buf, nil))
}

func logRecords(t *testing.T, buf *bytes.Buffer) []map[string]any {
	t.Helper()
	var out []map[string]any
	for _, line := range strings.Split(strings.TrimSpace(buf.String()), "\n") {
		if line == "" {
			continue
		}
		var rec map[string]any
		if err := json.Unmarshal([]byte(line), &rec); err != nil {
			t.Fatalf("log line is not JSON: %q", line)
		}
		out = append(out, rec)
	}
	return out
}

// P1-21: the client can only ever report the assistant's first response as one
// total, because `server_ts_ms` rides on `ai.text.delta` alone. The first
// *audio* frame — the one a learner perceives — has no server-side instant to
// subtract, so it cannot be split into upstream / server / downstream the way
// the first text frame can.
//
// The gateway owns the missing half without any protocol change: it knows when
// the turn started and when it emitted the first audio frame. Logging both
// gives the server's own segment, and the client's total minus it is the
// network's.
func TestVolcDuplexMarksTheFirstAudioFrameOfATurn(t *testing.T) {
	t.Parallel()

	sess, _ := streamableSession(t)
	var buf bytes.Buffer
	sess.logger = captureLogs(&buf)
	// A turn that started 120ms ago: the mark must report the gateway's own
	// segment, not zero and not the wall clock.
	sess.turnStarted = time.Now().Add(-120 * time.Millisecond)

	sess.AssistantAudio(make([]byte, 640))

	marks := 0
	for _, rec := range logRecords(t, &buf) {
		if rec["msg"] != "voice.duplex.first_audio" {
			continue
		}
		marks++
		serverMs, ok := rec["server_ms"].(float64)
		if !ok {
			t.Fatalf("server_ms missing or not a number: %#v", rec)
		}
		if serverMs < 100 {
			t.Fatalf("server_ms = %v, want >= 100 for a turn that started 120ms ago", serverMs)
		}
		if _, ok := rec["server_ts_ms"].(float64); !ok {
			t.Fatalf("server_ts_ms missing: %#v", rec)
		}
		if rec["turn_id"] != "turn-1" {
			t.Fatalf("turn_id = %v, want turn-1 (the mark has to join the client's log)", rec["turn_id"])
		}
	}
	if marks != 1 {
		t.Fatalf("emitted %d first_audio marks, want exactly 1", marks)
	}
}

// One mark per turn, not one per frame. Audio arrives in a burst, and a mark
// per frame would turn "when did the user start hearing it" into a rate.
func TestVolcDuplexMarksFirstAudioOncePerTurn(t *testing.T) {
	t.Parallel()

	sess, _ := streamableSession(t)
	var buf bytes.Buffer
	sess.logger = captureLogs(&buf)
	sess.turnStarted = time.Now()

	for range 5 {
		sess.AssistantAudio(make([]byte, 640))
	}

	marks := 0
	for _, rec := range logRecords(t, &buf) {
		if rec["msg"] == "voice.duplex.first_audio" {
			marks++
		}
	}
	if marks != 1 {
		t.Fatalf("emitted %d marks for 5 audio chunks, want 1", marks)
	}
}

// Empty chunks are not audio. A mark on one would report a first-audio instant
// for a frame the client never receives.
func TestVolcDuplexDoesNotMarkOnAnEmptyAudioChunk(t *testing.T) {
	t.Parallel()

	sess, _ := streamableSession(t)
	var buf bytes.Buffer
	sess.logger = captureLogs(&buf)
	sess.turnStarted = time.Now()

	sess.AssistantAudio(nil)

	if strings.Contains(buf.String(), "voice.duplex.first_audio") {
		t.Fatalf("marked a first audio frame for an empty chunk: %s", buf.String())
	}
}

// timeNow is the test-side clock, kept here so the marker tests do not each
// import time.
func timeNow() time.Time { return time.Now() }
