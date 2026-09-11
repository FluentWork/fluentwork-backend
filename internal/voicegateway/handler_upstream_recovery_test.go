package voicegateway_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/FluentWork/fluentwork-backend/internal/voicegateway"
	"github.com/FluentWork/fluentwork-backend/internal/voiceproto"
)

// scriptedProvider lets a test decide, at any moment, whether audio writes
// fail. Open() clears the flag and models a reopen that brings the upstream
// back. It exists to exercise the handler's reopen budget without a live Volc
// socket.
type scriptedProvider struct {
	mu    sync.Mutex
	fail  bool
	opens int
}

func (p *scriptedProvider) setFail(v bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.fail = v
}

func (p *scriptedProvider) openCount() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return p.opens
}

func (p *scriptedProvider) Open(_ context.Context, _ voicegateway.ConsumedTicket) (voicegateway.VoiceProviderSession, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.opens++
	p.fail = false // a reopen restores a healthy upstream
	return &scriptedSession{provider: p}, nil
}

type scriptedSession struct{ provider *scriptedProvider }

func (s *scriptedSession) Start(_ context.Context, _ voiceproto.SessionStart, _ []voicegateway.ContinuationTurn) ([]voicegateway.ProviderOutbound, error) {
	return []voicegateway.ProviderOutbound{
		{Control: map[string]any{"type": voiceproto.TypeAITextDelta, "text": "ready"}},
		{Control: voiceproto.AITurnEnd{Type: voiceproto.TypeAITurnEnd}},
	}, nil
}

func (s *scriptedSession) HandleClientControl(_ context.Context, _ string, _ []byte) ([]voicegateway.ProviderOutbound, error) {
	return nil, nil
}

func (s *scriptedSession) HandleClientAudio(_ context.Context, _ []byte) ([]voicegateway.ProviderOutbound, error) {
	s.provider.mu.Lock()
	fail := s.provider.fail
	s.provider.mu.Unlock()
	if fail {
		return nil, errors.New("failed to write frame: use of closed network connection")
	}
	return nil, nil
}

func (s *scriptedSession) SnapshotUtterances() []voicegateway.EndUtterance { return nil }
func (s *scriptedSession) Close(_ context.Context) error                   { return nil }

// assertSessionAlive pings the gateway and requires a pong. A session that took
// the fatal audio-forward path has already been torn down, so this fails
// positively rather than by timing out on silence.
func assertSessionAlive(ctx context.Context, t *testing.T, conn *websocket.Conn, when string) {
	t.Helper()
	if err := conn.Write(ctx, websocket.MessageText, voiceproto.MustMarshal(voiceproto.Ping{
		Type: voiceproto.TypePing, TS: 1,
	})); err != nil {
		t.Fatalf("session is gone %s: ping write failed: %v", when, err)
	}
	frame := readFrame(ctx, t, conn)
	if frame["type"] != voiceproto.TypePong {
		t.Fatalf("expected pong %s, got %#v", when, frame)
	}
}

// The transparent reopen was one-shot for the whole session: reopenAttempted
// was set on the first recovery and never cleared, so any further upstream
// failure killed the session outright. Measured against volc-duplex the
// upstream dies roughly once per turn (2026-09-10 physical-device logs), which
// is why the third turn always ended the session. The budget belongs to a
// turn, not to the session.
func TestHandler_ReopenBudgetRefillsPerTurn(t *testing.T) {
	t.Parallel()

	consumer := &stubConsumer{
		ticket: "good-ticket",
		out:    voicegateway.ConsumedTicket{TicketID: "t1", SessionID: "s1", UserID: "u1"},
	}
	provider := &scriptedProvider{}
	h := voicegateway.NewHandler(consumer, &stubLifecycle{}, provider, nil,
		voicegateway.Options{InsecureSkipOrigin: true})
	mux := http.NewServeMux()
	h.Mount(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn := dialVoice(ctx, t, srv)
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()

	authAndStart(ctx, t, conn)

	// Turn 1: the upstream dies mid-turn. The single reopen recovers it.
	provider.setFail(true)
	if err := conn.Write(ctx, websocket.MessageBinary, []byte{1, 2, 3, 4}); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	assertSessionAlive(ctx, t, conn, "after the first upstream failure")

	// Turn 2 begins. A fresh turn gets a fresh budget.
	if err := conn.Write(ctx, websocket.MessageText, voiceproto.MustMarshal(voiceproto.UserSpeechStart{
		Type: voiceproto.TypeUserSpeechStart,
	})); err != nil {
		t.Fatalf("write user.speech.start: %v", err)
	}

	// The upstream dies again on the new turn.
	provider.setFail(true)
	if err := conn.Write(ctx, websocket.MessageBinary, []byte{5, 6, 7, 8}); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	assertSessionAlive(ctx, t, conn, "after the second upstream failure")

	if got := provider.openCount(); got != 3 {
		t.Fatalf("provider opens = %d, want 3 (initial + one reopen per turn)", got)
	}
}
