package voicegateway_test

import (
	"context"
	"encoding/binary"
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

// sequencedProvider models the one property of volc-duplex that matters here:
// Open returns a brand-new session object, so its frame counter starts at zero.
type sequencedProvider struct {
	mu    sync.Mutex
	fail  bool
	opens int
}

func (p *sequencedProvider) setFail(v bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.fail = v
}

func (p *sequencedProvider) Open(_ context.Context, _ voicegateway.ConsumedTicket) (voicegateway.VoiceProviderSession, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.opens++
	p.fail = false // a reopen restores a healthy upstream
	return &sequencedSession{provider: p}, nil
}

// sequencedSession numbers every binary frame it emits, the way
// volcDuplexProviderSession does with nextAudioSeq. The assertion matters: if
// this double stops satisfying the interface, the handler's carry becomes a
// no-op and the test would pass while asserting nothing.
var _ voicegateway.SequencedVoiceProviderSession = (*sequencedSession)(nil)

type sequencedSession struct {
	provider *sequencedProvider
	seq      uint32
}

func (s *sequencedSession) Start(_ context.Context, _ voiceproto.SessionStart) ([]voicegateway.ProviderOutbound, error) {
	return []voicegateway.ProviderOutbound{
		{Control: map[string]any{"type": voiceproto.TypeAITextDelta, "text": "ready"}},
		{Control: voiceproto.AITurnEnd{Type: voiceproto.TypeAITurnEnd}},
	}, nil
}

func (s *sequencedSession) HandleClientControl(_ context.Context, _ string, _ []byte) ([]voicegateway.ProviderOutbound, error) {
	return nil, nil
}

func (s *sequencedSession) HandleClientAudio(_ context.Context, _ []byte) ([]voicegateway.ProviderOutbound, error) {
	s.provider.mu.Lock()
	fail := s.provider.fail
	s.provider.mu.Unlock()
	if fail {
		return nil, errors.New("failed to write frame: use of closed network connection")
	}

	s.seq++
	frame := make([]byte, 4+3)
	binary.BigEndian.PutUint32(frame, s.seq)
	copy(frame[4:], []byte{0xAA, 0xBB, 0xCC})
	return []voicegateway.ProviderOutbound{{Binary: frame}}, nil
}

func (s *sequencedSession) SnapshotUtterances() []voicegateway.EndUtterance { return nil }
func (s *sequencedSession) Close(_ context.Context) error                   { return nil }

func (s *sequencedSession) NextAudioSequence() uint32   { return s.seq }
func (s *sequencedSession) AdoptAudioSequence(v uint32) { s.seq = v }

func readBinaryFrame(ctx context.Context, t *testing.T, conn *websocket.Conn) []byte {
	t.Helper()
	typ, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if typ != websocket.MessageBinary {
		t.Fatalf("expected a binary frame, got %v (%s)", typ, data)
	}
	return data
}

func frameSequence(data []byte) uint32 {
	if len(data) < 4 {
		return 0
	}
	return binary.BigEndian.Uint32(data[:4])
}

// The client drops every audio frame at or below its barge-in watermark, and
// that watermark lives for the whole WS session — it is only cleared by
// start/stopCapture, never between turns. The gateway numbers its frames
// monotonically *because* of that (see nextAudioSeq's comment), but a
// transparent reopen builds a fresh provider session, so the numbering
// restarted at 1 behind a watermark already in the hundreds.
//
// Every later frame was then discarded: the transcript kept working (it does
// not go through the gate) and all audio was gone for the rest of the run,
// which is exactly what the physical-device report shows.
func TestHandler_AudioSequenceSurvivesTransparentReopen(t *testing.T) {
	t.Parallel()

	consumer := &stubConsumer{
		ticket: "good-ticket",
		out:    voicegateway.ConsumedTicket{TicketID: "t1", SessionID: "s1", UserID: "u1"},
	}
	provider := &sequencedProvider{}
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

	// Three frames go out, numbered 1..3.
	var lastBeforeReopen uint32
	for i := 0; i < 3; i++ {
		if err := conn.Write(ctx, websocket.MessageBinary, []byte{1, 2, 3, 4}); err != nil {
			t.Fatalf("write binary: %v", err)
		}
		lastBeforeReopen = frameSequence(readBinaryFrame(ctx, t, conn))
	}
	if lastBeforeReopen != 3 {
		t.Fatalf("frames before the reopen numbered up to %d, want 3", lastBeforeReopen)
	}

	// The upstream dies. The handler reopens transparently and retries the
	// chunk — and that retried chunk is numbered by the NEW provider session.
	provider.setFail(true)
	if err := conn.Write(ctx, websocket.MessageBinary, []byte{5, 6, 7, 8}); err != nil {
		t.Fatalf("write binary: %v", err)
	}
	afterReopen := frameSequence(readBinaryFrame(ctx, t, conn))

	if afterReopen <= lastBeforeReopen {
		t.Fatalf(
			"frame after the reopen is numbered %d, at or below the %d already sent: "+
				"the client discards every frame at or below its barge-in watermark, "+
				"so the rest of the session would be silent",
			afterReopen, lastBeforeReopen,
		)
	}
}
