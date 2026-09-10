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

// emitterTrackingSession records whether the handler installed a push path.
//
// The failure this guards is silent: a provider that streams only suppresses
// its end-of-turn text frame when a sink is installed, so a session with no
// emitter keeps working and simply never streams. Nothing errors, nothing logs.
type emitterTrackingSession struct {
	mu       sync.Mutex
	emit     func(voicegateway.ProviderOutbound) error
	failNext bool
}

var _ voicegateway.StreamingVoiceProviderSession = (*emitterTrackingSession)(nil)

func (s *emitterTrackingSession) Start(context.Context, voiceproto.SessionStart) ([]voicegateway.ProviderOutbound, error) {
	// Two frames because that is what the session bootstrap puts on the wire
	// (see authAndStart): a greeting and the turn-end that closes it.
	return []voicegateway.ProviderOutbound{
		{Control: map[string]any{"type": voiceproto.TypeAITextDelta, "text": "ready"}},
		{Control: voiceproto.AITurnEnd{Type: voiceproto.TypeAITurnEnd}},
	}, nil
}

func (s *emitterTrackingSession) HandleClientControl(context.Context, string, []byte) ([]voicegateway.ProviderOutbound, error) {
	return nil, nil
}

func (s *emitterTrackingSession) HandleClientAudio(context.Context, []byte) ([]voicegateway.ProviderOutbound, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.failNext {
		s.failNext = false
		// The shape the handler treats as a dead upstream, which triggers the
		// transparent reopen.
		return nil, errors.New("failed to write frame: use of closed network connection")
	}
	return nil, nil
}

func (s *emitterTrackingSession) SnapshotUtterances() []voicegateway.EndUtterance { return nil }
func (s *emitterTrackingSession) Close(context.Context) error                     { return nil }

func (s *emitterTrackingSession) SetOutboundEmitter(emit func(voicegateway.ProviderOutbound) error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.emit = emit
}

func (s *emitterTrackingSession) emitterInstalled() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.emit != nil
}

func (s *emitterTrackingSession) killUpstream() {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.failNext = true
}

type emitterTrackingProvider struct {
	mu       sync.Mutex
	sessions []*emitterTrackingSession
}

func (p *emitterTrackingProvider) Open(context.Context, voicegateway.ConsumedTicket) (voicegateway.VoiceProviderSession, error) {
	sess := &emitterTrackingSession{}
	p.mu.Lock()
	p.sessions = append(p.sessions, sess)
	p.mu.Unlock()
	return sess, nil
}

func (p *emitterTrackingProvider) session(i int) *emitterTrackingSession {
	p.mu.Lock()
	defer p.mu.Unlock()
	if i >= len(p.sessions) {
		return nil
	}
	return p.sessions[i]
}

func (p *emitterTrackingProvider) count() int {
	p.mu.Lock()
	defer p.mu.Unlock()
	return len(p.sessions)
}

// The emitter has to be attached everywhere rt.provider is (re)assigned. A
// transparent reopen builds a brand-new session, and forgetting it there would
// not break anything visibly — it would just stop streaming for the rest of the
// run, with no error to notice.
func TestHandler_InstallsTheOutboundEmitterOnEverySessionItOpens(t *testing.T) {
	t.Parallel()

	consumer := &stubConsumer{
		ticket: "good-ticket",
		out:    voicegateway.ConsumedTicket{TicketID: "t1", SessionID: "s1", UserID: "u1"},
	}
	provider := &emitterTrackingProvider{}
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

	first := provider.session(0)
	if first == nil {
		t.Fatal("session.start did not open a provider session")
	}
	if !first.emitterInstalled() {
		t.Fatal("the first session got no emitter; the assistant's reply would never stream")
	}

	// Kill the upstream and send audio: the handler reopens transparently and
	// retries the chunk against a fresh session.
	first.killUpstream()
	if err := conn.Write(ctx, websocket.MessageBinary, []byte{1, 2, 3, 4}); err != nil {
		t.Fatalf("write binary: %v", err)
	}

	deadline := time.Now().Add(3 * time.Second)
	for provider.count() < 2 && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if provider.count() < 2 {
		t.Fatalf("no reopen happened (%d sessions); the test cannot check the reopen site", provider.count())
	}

	if !provider.session(1).emitterInstalled() {
		t.Fatal("the reopened session got no emitter; streaming would stop silently for the rest of the run")
	}
}
