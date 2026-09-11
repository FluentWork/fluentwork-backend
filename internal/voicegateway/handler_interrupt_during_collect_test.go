package voicegateway_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/FluentWork/fluentwork-backend/internal/voicegateway"
	"github.com/FluentWork/fluentwork-backend/internal/voiceproto"
)

// blockingCollectSession holds user.speech.end until the test releases it,
// so we can see whether interrupt is readable while collectTurn is in flight.
type blockingCollectSession struct {
	mu         sync.Mutex
	controls   []string
	endEntered chan struct{}
	releaseEnd chan struct{}
	endOnce    sync.Once
}

func (s *blockingCollectSession) Start(_ context.Context, _ voiceproto.SessionStart, _ []voicegateway.ContinuationTurn) ([]voicegateway.ProviderOutbound, error) {
	return []voicegateway.ProviderOutbound{
		{Control: voiceproto.NewAITextDelta("ready", "bootstrap", time.Now().UnixMilli())},
		{Control: voiceproto.AITurnEnd{Type: voiceproto.TypeAITurnEnd, TurnID: "bootstrap", Outcome: "ok"}},
	}, nil
}

func (s *blockingCollectSession) HandleClientControl(ctx context.Context, frameType string, _ []byte) ([]voicegateway.ProviderOutbound, error) {
	s.mu.Lock()
	s.controls = append(s.controls, frameType)
	s.mu.Unlock()

	if frameType != voiceproto.TypeUserSpeechEnd {
		return nil, nil
	}
	s.endOnce.Do(func() { close(s.endEntered) })
	select {
	case <-s.releaseEnd:
	case <-ctx.Done():
		return nil, ctx.Err()
	}
	return []voicegateway.ProviderOutbound{
		{Control: voiceproto.AITurnEnd{Type: voiceproto.TypeAITurnEnd, TurnID: "turn-1", Outcome: "ok"}},
	}, nil
}

func (s *blockingCollectSession) HandleClientAudio(context.Context, []byte) ([]voicegateway.ProviderOutbound, error) {
	return nil, nil
}
func (s *blockingCollectSession) SnapshotUtterances() []voicegateway.EndUtterance { return nil }
func (s *blockingCollectSession) Close(context.Context) error                     { return nil }

func (s *blockingCollectSession) snapshotControls() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	out := make([]string, len(s.controls))
	copy(out, s.controls)
	return out
}

type blockingCollectProvider struct {
	session *blockingCollectSession
}

func (p *blockingCollectProvider) Open(context.Context, voicegateway.ConsumedTicket) (voicegateway.VoiceProviderSession, error) {
	return p.session, nil
}

// The WSS read loop used to call WaitTurnResult on the same goroutine as
// conn.Read. An interrupt that arrived while the assistant was still
// generating sat in the TCP buffer until collectTurn returned — by then
// turnToOutbound had already persisted the full reply and every leftover
// audio frame had already been forwarded. Barge-in during TTS was a no-op
// on the server (2026-09-12).
func TestHandler_ProcessesInterruptWhileSpeechEndIsCollecting(t *testing.T) {
	t.Parallel()

	sess := &blockingCollectSession{
		endEntered: make(chan struct{}),
		releaseEnd: make(chan struct{}),
	}
	consumer := &stubConsumer{
		ticket: "good-ticket",
		out:    voicegateway.ConsumedTicket{TicketID: "t1", SessionID: "s1", UserID: "u1"},
	}
	h := voicegateway.NewHandler(consumer, &stubLifecycle{}, &blockingCollectProvider{session: sess}, nil, voicegateway.Options{InsecureSkipOrigin: true})
	mux := http.NewServeMux()
	h.Mount(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn := dialVoice(ctx, t, srv)
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()
	authAndStart(ctx, t, conn)

	if err := conn.Write(ctx, websocket.MessageText, voiceproto.MustMarshal(voiceproto.UserSpeechEnd{
		Type:   voiceproto.TypeUserSpeechEnd,
		TurnID: "turn-1",
	})); err != nil {
		t.Fatalf("write user.speech.end: %v", err)
	}

	select {
	case <-sess.endEntered:
	case <-ctx.Done():
		t.Fatal("user.speech.end never reached the provider")
	}

	if err := conn.Write(ctx, websocket.MessageText, voiceproto.MustMarshal(voiceproto.Interrupt{
		Type: voiceproto.TypeInterrupt,
	})); err != nil {
		t.Fatalf("write interrupt: %v", err)
	}

	deadline := time.Now().Add(400 * time.Millisecond)
	for time.Now().Before(deadline) {
		for _, typ := range sess.snapshotControls() {
			if typ == voiceproto.TypeInterrupt {
				close(sess.releaseEnd)
				turnEnd := readFrame(ctx, t, conn)
				if turnEnd["type"] != voiceproto.TypeAITurnEnd {
					t.Fatalf("expected ai.turn.end after collect, got %#v", turnEnd)
				}
				return
			}
		}
		time.Sleep(10 * time.Millisecond)
	}

	close(sess.releaseEnd)
	t.Fatalf("interrupt was not processed while user.speech.end was still collecting; controls=%v", sess.snapshotControls())
}
