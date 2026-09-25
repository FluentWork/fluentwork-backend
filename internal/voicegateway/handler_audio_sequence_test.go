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
// Open returns a brand-new session object, so anything the session keeps in a
// field starts over.
//
// Note what is NOT a field here any more. The session used to hold its own
// counter, and the handler carried the value across a reopen
// (`carryAudioSequence`) — which is exactly the step that got forgotten in
// production. The allocator is now handed in by Open and shared by every session
// for one client, so there is no step to forget.
type sequencedProvider struct {
	mu    sync.Mutex
	fail  bool
	opens int
	// seq records what Open was handed, so the test can assert the SAME
	// allocator reaches the replacement session rather than a fresh one.
	seq *voicegateway.SeqAllocator
}

func (p *sequencedProvider) setFail(v bool) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.fail = v
}

func (p *sequencedProvider) Open(_ context.Context, _ voicegateway.ConsumedTicket, audioSeq *voicegateway.SeqAllocator, _ *voicegateway.TurnRefAllocator) (voicegateway.VoiceProviderSession, error) {
	p.mu.Lock()
	defer p.mu.Unlock()
	p.opens++
	p.fail = false // a reopen restores a healthy upstream
	p.seq = audioSeq
	return &sequencedSession{provider: p, audioSeq: audioSeq}, nil
}

// sequencedSession numbers every binary frame it emits through the session's
// allocator, the way volcDuplexProviderSession does. If this double stopped using
// the allocator and kept its own counter, the test would pass while asserting
// nothing — the counter would restart on reopen and the assertion below would
// catch it only because the numbers are read off the wire.
type sequencedSession struct {
	provider *sequencedProvider
	audioSeq *voicegateway.SeqAllocator
}

func (s *sequencedSession) Start(_ context.Context, _ voiceproto.SessionStart, _ []voicegateway.ContinuationTurn) ([]voicegateway.ProviderOutbound, error) {
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

	frame := make([]byte, 4+3)
	binary.BigEndian.PutUint32(frame, s.audioSeq.Next())
	copy(frame[4:], []byte{0xAA, 0xBB, 0xCC})
	return []voicegateway.ProviderOutbound{{Binary: frame}}, nil
}

func (s *sequencedSession) SnapshotUtterances() []voicegateway.EndUtterance { return nil }
func (s *sequencedSession) Close(_ context.Context) error                   { return nil }

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

	// The mechanism, asserted directly: the replacement session was handed the
	// same allocator. Without this, a future change could pass a fresh allocator
	// and the number check above would only tell us after the fact.
	provider.mu.Lock()
	shared := provider.seq
	provider.mu.Unlock()
	if shared == nil {
		t.Fatal("the provider was never handed an allocator")
	}
	if got := shared.Peek(); got < afterReopen {
		t.Fatalf("allocator last handed out %d, but frame %d reached the client: they disagree", got, afterReopen)
	}

	if afterReopen <= lastBeforeReopen {
		t.Fatalf(
			"frame after the reopen is numbered %d, at or below the %d already sent: "+
				"the client discards every frame at or below its barge-in watermark, "+
				"so the rest of the session would be silent",
			afterReopen, lastBeforeReopen,
		)
	}
}
