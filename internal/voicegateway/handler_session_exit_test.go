package voicegateway_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/voicegateway"
	"github.com/FluentWork/fluentwork-backend/internal/voiceproto"
	"github.com/coder/websocket"
)

// signalingLifecycle records end requests and signals on a channel, so a test
// asserting the gateway's exit path has a real happens-before edge instead of
// sleeping on shared state.
type signalingLifecycle struct {
	mu        sync.Mutex
	endCalls  int
	activateN int
	ended     chan voicegateway.EndSessionRequest
}

func newSignalingLifecycle() *signalingLifecycle {
	return &signalingLifecycle{ended: make(chan voicegateway.EndSessionRequest, 4)}
}

func (s *signalingLifecycle) Activate(_ context.Context, _ string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.activateN++
	return nil
}

func (s *signalingLifecycle) End(_ context.Context, req voicegateway.EndSessionRequest) error {
	s.mu.Lock()
	s.endCalls++
	s.mu.Unlock()
	s.ended <- req
	return nil
}

func (s *signalingLifecycle) calls() int {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.endCalls
}

// failingAudioSession fails every audio write, so the handler takes the
// provider_audio_failed exit path. It reports utterances so the test can prove
// they survive that exit.
type failingAudioSession struct{}

func (s *failingAudioSession) Start(_ context.Context, _ voiceproto.SessionStart) ([]voicegateway.ProviderOutbound, error) {
	return []voicegateway.ProviderOutbound{
		{Control: map[string]any{"type": voiceproto.TypeAITextDelta, "text": "ready"}},
		{Control: voiceproto.AITurnEnd{Type: voiceproto.TypeAITurnEnd}},
	}, nil
}

func (s *failingAudioSession) HandleClientControl(_ context.Context, _ string, _ []byte) ([]voicegateway.ProviderOutbound, error) {
	return nil, nil
}

func (s *failingAudioSession) HandleClientAudio(_ context.Context, _ []byte) ([]voicegateway.ProviderOutbound, error) {
	return nil, errors.New("duplex write: use of closed network connection")
}

func (s *failingAudioSession) SnapshotUtterances() []voicegateway.EndUtterance {
	return []voicegateway.EndUtterance{
		{Seq: 1, Speaker: "user", Text: "我尝试说。"},
		{Seq: 2, Speaker: "ai", Text: "大胆说就好。"},
	}
}

func (s *failingAudioSession) Close(_ context.Context) error { return nil }

type failingAudioProvider struct{ session *failingAudioSession }

func (p *failingAudioProvider) Open(_ context.Context, _ voicegateway.ConsumedTicket) (voicegateway.VoiceProviderSession, error) {
	if p.session == nil {
		p.session = &failingAudioSession{}
	}
	return p.session, nil
}

// A session killed by a provider failure used to vanish: the client never sent
// `session.end`, and the only lifecycle.End call site was that control frame.
// The utterances were lost with it — no rows, no session.finished job, no review.
func TestHandler_ProviderFailurePersistsSessionOnExit(t *testing.T) {
	t.Parallel()

	consumer := &stubConsumer{
		ticket: "good-ticket",
		out:    voicegateway.ConsumedTicket{TicketID: "t1", SessionID: "s1", UserID: "u1"},
	}
	life := newSignalingLifecycle()
	h := voicegateway.NewHandler(consumer, life, &failingAudioProvider{}, nil,
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

	// First binary frame fails the write; the reopen retry fails too and the
	// handler reports provider_audio_failed and exits the loop.
	if err := conn.Write(ctx, websocket.MessageBinary, []byte{1, 2, 3, 4}); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	errFrame := readFrame(ctx, t, conn)
	if errFrame["type"] != voiceproto.TypeError || errFrame["code"] != "provider_audio_failed" {
		t.Fatalf("expected provider_audio_failed error frame, got %#v", errFrame)
	}

	select {
	case req := <-life.ended:
		if req.SessionID != "s1" {
			t.Fatalf("session_id = %q, want s1", req.SessionID)
		}
		if req.Reason != "provider_audio_failed" {
			t.Fatalf("reason = %q, want provider_audio_failed", req.Reason)
		}
		if len(req.Utterances) != 2 || req.Utterances[0].Text != "我尝试说。" {
			t.Fatalf("utterances were not carried through the exit path: %+v", req.Utterances)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("provider failure did not persist the session on exit")
	}

	if got := life.calls(); got != 1 {
		t.Fatalf("lifecycle end calls = %d, want exactly 1", got)
	}
}

// The client's own `session.end` already persists. The loop exit path must not
// write a second time.
func TestHandler_ClientSessionEndPersistsExactlyOnce(t *testing.T) {
	t.Parallel()

	consumer := &stubConsumer{
		ticket: "good-ticket",
		out:    voicegateway.ConsumedTicket{TicketID: "t1", SessionID: "s1", UserID: "u1"},
	}
	life := newSignalingLifecycle()
	h := voicegateway.NewHandler(consumer, life, &stubProvider{}, nil,
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

	if err := conn.Write(ctx, websocket.MessageText, voiceproto.MustMarshal(voiceproto.SessionEnd{
		Type: voiceproto.TypeSessionEnd, Reason: "user",
	})); err != nil {
		t.Fatalf("write session.end: %v", err)
	}
	if ack := readFrame(ctx, t, conn); ack["type"] != voiceproto.TypeSessionEnd {
		t.Fatalf("expected session.end ack, got %#v", ack)
	}

	select {
	case req := <-life.ended:
		if req.Reason != "user" {
			t.Fatalf("reason = %q, want user", req.Reason)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("client session.end did not persist")
	}

	// The loop unwinds after the ack; any second write would land here.
	select {
	case <-life.ended:
		t.Fatal("session was persisted twice (client session.end + exit path)")
	case <-time.After(500 * time.Millisecond):
	}

	if got := life.calls(); got != 1 {
		t.Fatalf("lifecycle end calls = %d, want exactly 1", got)
	}
}

// A connection that never sent session.start has nothing to persist; the exit
// path must stay quiet.
func TestHandler_ExitWithoutSessionStartDoesNotPersist(t *testing.T) {
	t.Parallel()

	consumer := &stubConsumer{
		ticket: "good-ticket",
		out:    voicegateway.ConsumedTicket{TicketID: "t1", SessionID: "s1", UserID: "u1"},
	}
	life := newSignalingLifecycle()
	h := voicegateway.NewHandler(consumer, life, &stubProvider{}, nil,
		voicegateway.Options{InsecureSkipOrigin: true})
	mux := http.NewServeMux()
	h.Mount(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn := dialVoice(ctx, t, srv)
	if err := conn.Write(ctx, websocket.MessageText, voiceproto.MustMarshal(voiceproto.Auth{
		Type: voiceproto.TypeAuth, Ticket: "good-ticket",
	})); err != nil {
		t.Fatalf("write auth: %v", err)
	}
	_ = readFrame(ctx, t, conn) // session.ready
	_ = conn.Close(websocket.StatusNormalClosure, "")

	select {
	case req := <-life.ended:
		t.Fatalf("unstarted connection was persisted: %+v", req)
	case <-time.After(500 * time.Millisecond):
	}

	if got := life.calls(); got != 0 {
		t.Fatalf("lifecycle end calls = %d, want 0", got)
	}
}
