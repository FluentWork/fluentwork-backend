package voicegateway_test

import (
	"context"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/FluentWork/fluentwork-backend/internal/voicegateway"
	"github.com/FluentWork/fluentwork-backend/internal/voiceproto"
)

// recordingHandler is a minimal slog.Handler that captures every Record into
// a slice so tests can assert on Warn frequency. B15: the bug under test is
// "80 identical warn lines per session"; we need a counter to prove that
// dedup actually happens.
type recordingHandler struct {
	mu      sync.Mutex
	records []slog.Record
}

func (h *recordingHandler) Enabled(_ context.Context, _ slog.Level) bool { return true }

func (h *recordingHandler) Handle(_ context.Context, r slog.Record) error {
	h.mu.Lock()
	defer h.mu.Unlock()
	h.records = append(h.records, r.Clone())
	return nil
}

func (h *recordingHandler) WithAttrs(_ []slog.Attr) slog.Handler { return h }
func (h *recordingHandler) WithGroup(_ string) slog.Handler      { return h }

func (h *recordingHandler) warnCount(msgSubstring string) int {
	h.mu.Lock()
	defer h.mu.Unlock()
	count := 0
	for _, r := range h.records {
		if r.Level < slog.LevelWarn {
			continue
		}
		if strings.Contains(r.Message, msgSubstring) {
			count++
		}
	}
	return count
}

// brokenProviderSession fails every HandleClientAudio call so the handler
// should mark the runtime broken and stop invoking the provider.
type brokenProviderSession struct {
	audioCalls int32
}

func (s *brokenProviderSession) Start(_ context.Context, _ voiceproto.SessionStart) ([]voicegateway.ProviderOutbound, error) {
	return []voicegateway.ProviderOutbound{
		{Control: map[string]any{"type": voiceproto.TypeAITextDelta, "text": "ready"}},
		{Control: voiceproto.AITurnEnd{Type: voiceproto.TypeAITurnEnd}},
	}, nil
}

func (s *brokenProviderSession) HandleClientControl(_ context.Context, _ string, _ []byte) ([]voicegateway.ProviderOutbound, error) {
	return nil, nil
}

func (s *brokenProviderSession) HandleClientAudio(_ context.Context, _ []byte) ([]voicegateway.ProviderOutbound, error) {
	atomic.AddInt32(&s.audioCalls, 1)
	return nil, &stubBrokenError{}
}

func (s *brokenProviderSession) SnapshotUtterances() []voicegateway.EndUtterance { return nil }
func (s *brokenProviderSession) Close(_ context.Context) error                   { return nil }

// brokenProvider wraps a single brokenProviderSession for voicegateway.VoiceProvider.
type brokenProvider struct{ session *brokenProviderSession }

func (p *brokenProvider) Open(_ context.Context, _ voicegateway.ConsumedTicket) (voicegateway.VoiceProviderSession, error) {
	return p.session, nil
}

type stubBrokenError struct{}

func (e *stubBrokenError) Error() string { return "stub: provider write to volcengine failed" }

// TestHandler_AudioMarksSessionBrokenAfterFirstFailure is the B15 regression
// for docs/20 §1.2.c / §1.3 P1, updated for the #43 reopen-once behavior.
// We send N binary frames after session.start; the provider's
// HandleClientAudio fails on every call. After the first failure the handler
// reopens the provider once and retries (2 calls total), then must:
//
//  1. Stop calling the provider (audio call count capped at 2)
//  2. Log exactly ONE warn line for the cascade (not N)
//  3. Still successfully write the error frame to the iOS WS once
//
// Without the broken-flag, the gateway produced 80+ duplicate warns per
// session, drowning the actual root cause in noise.
func TestHandler_AudioMarksSessionBrokenAfterFirstFailure(t *testing.T) {
	t.Parallel()

	consumer := &stubConsumer{
		ticket: "good-ticket",
		out: voicegateway.ConsumedTicket{
			TicketID: "t1", SessionID: "s1", UserID: "u1",
		},
	}
	providerSession := &brokenProviderSession{}
	provider := &brokenProvider{session: providerSession}

	recLog := &recordingHandler{}
	logger := slog.New(recLog)

	h := voicegateway.NewHandler(consumer, &stubLifecycle{}, provider, logger, voicegateway.Options{InsecureSkipOrigin: true})
	mux := http.NewServeMux()
	h.Mount(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/voice"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()

	if err := conn.Write(ctx, websocket.MessageText, voiceproto.MustMarshal(voiceproto.Auth{
		Type: voiceproto.TypeAuth, Ticket: "good-ticket",
	})); err != nil {
		t.Fatalf("write auth: %v", err)
	}
	_ = readFrame(ctx, t, conn) // session.ready

	if err := conn.Write(ctx, websocket.MessageText, voiceproto.MustMarshal(voiceproto.SessionStart{
		Type: voiceproto.TypeSessionStart,
	})); err != nil {
		t.Fatalf("write session.start: %v", err)
	}
	_ = readFrame(ctx, t, conn) // ai.text.delta
	_ = readFrame(ctx, t, conn) // ai.turn.end

	// Send N binary frames; the broken provider returns an error every time.
	const totalFrames = 8
	for i := 0; i < totalFrames; i++ {
		if err := conn.Write(ctx, websocket.MessageBinary, []byte{1, 2, 3, 4}); err != nil {
			t.Fatalf("write binary %d: %v", i, err)
		}
	}

	// Expect exactly one provider_audio_failed error frame (the first failure),
	// then subsequent frames are dropped silently by the broken guard.
	errFrame := readFrame(ctx, t, conn)
	if errFrame["type"] != voiceproto.TypeError {
		t.Fatalf("expected error frame after first binary failure, got %#v", errFrame)
	}
	if errFrame["code"] != "provider_audio_failed" {
		t.Fatalf("expected provider_audio_failed code, got %#v", errFrame)
	}

	// No more frames should arrive on the iOS side — the broken guard drops them.
	// Item 1.2: the handler returns an error after the first failure, which causes
	// the loop to exit and the connection to be closed by the server. The iOS side
	// will see either a timeout (if the connection is still alive but silent) or
	// a connection reset (if the server closed the socket).
	readCtx, readCancel := context.WithTimeout(ctx, 400*time.Millisecond)
	defer readCancel()
	_, _, readErr := conn.Read(readCtx)
	// Accept either a timeout (broken guard is working) or a connection error
	// (server closed the connection per Item 1.2 behavior).
	if readErr == nil {
		t.Fatal("expected no further frames after first failure (broken guard)")
	} else if !isTimeout(readErr) && !isConnectionReset(readErr) {
		t.Fatalf("expected timeout or connection reset after first failure, got %v", readErr)
	}

	// Assertion 1: provider HandleClientAudio called once for the original
	// frame plus once for the reopen retry; later frames are dropped.
	if got := atomic.LoadInt32(&providerSession.audioCalls); got != 2 {
		t.Fatalf("provider HandleClientAudio calls: got %d want 2 (broken guard failed to stop retries)", got)
	}

	// Assertion 2: exactly one B15 warn line for the cascade.
	if got := recLog.warnCount("provider audio forward failed"); got != 1 {
		t.Fatalf("B15 dedup warn count: got %d want 1", got)
	}
}

// isTimeout is a small helper because coder/websocket wraps the underlying
// net.Error with status info; we just want to know if the read timed out.
func isTimeout(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "i/o timeout") ||
		strings.Contains(s, "deadline exceeded") ||
		strings.Contains(s, "context deadline")
}

// isConnectionReset returns true when the WebSocket read fails because the
// server closed the connection (Item 1.2: handler returns error, loop exits,
// connection is closed).
func isConnectionReset(err error) bool {
	if err == nil {
		return false
	}
	s := err.Error()
	return strings.Contains(s, "connection reset") ||
		strings.Contains(s, "read: connection reset") ||
		strings.Contains(s, "failed to read frame header")
}
