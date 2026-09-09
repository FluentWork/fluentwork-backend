package voicegateway

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"testing"
	"time"
)

// recordingLogHandler captures slog records so B15 can prove logWarn collapses
// a burst of identical warnings instead of emitting one line per audio frame.
type recordingLogHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *recordingLogHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *recordingLogHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}

func (h *recordingLogHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *recordingLogHandler) WithGroup(_ string) slog.Handler      { return h }

func (h *recordingLogHandler) warnMessages(substr string) []string {
	h.mu.Lock()
	defer h.mu.Unlock()
	var out []string
	for _, r := range h.records {
		if r.Level < slog.LevelWarn {
			continue
		}
		if strings.Contains(r.Message, substr) {
			out = append(out, r.Message)
		}
	}
	return out
}

// TestLogWarn_SameKeyElevenTimesEmitsTwoLines is the i20 §2.4 unit proof:
// 11 calls with the same key inside the 5s window must produce the first
// full WARN plus one "(deduplicated)" summary at count 10 — not 11 lines.
func TestLogWarn_SameKeyElevenTimesEmitsTwoLines(t *testing.T) {
	t.Parallel()

	rec := &recordingLogHandler{}
	h := NewHandler(nil, nil, MockVoiceProvider{}, slog.New(rec), Options{InsecureSkipOrigin: true})
	frozen := time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC)
	h.now = func() time.Time { return frozen }

	rt := &sessionRuntime{}
	for i := 0; i < 11; i++ {
		h.logWarn(rt, "provider_audio_failed", "provider audio forward failed", "n", i)
	}

	msgs := rec.warnMessages("provider audio forward failed")
	if len(msgs) != 2 {
		t.Fatalf("warn count = %d want 2; msgs=%q", len(msgs), msgs)
	}
	if msgs[0] != "provider audio forward failed" {
		t.Fatalf("first warn = %q", msgs[0])
	}
	if !strings.Contains(msgs[1], "(deduplicated)") {
		t.Fatalf("second warn should be summary, got %q", msgs[1])
	}
	if rt.warnDedup.count != 11 {
		t.Fatalf("dedup count = %d want 11", rt.warnDedup.count)
	}
}
