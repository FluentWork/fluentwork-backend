package voicegateway_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/FluentWork/fluentwork-backend/internal/voicegateway"
	"github.com/FluentWork/fluentwork-backend/internal/voiceproto"
)

// refusingProvider is a provider whose every control call fails. It exists to
// make the failure policy observable: with a provider that never fails, "what
// happens when forwarding fails" is unanswerable from the outside.
type refusingProvider struct{}

func (refusingProvider) Open(context.Context, voicegateway.ConsumedTicket, *voicegateway.SeqAllocator, *voicegateway.TurnRefAllocator) (voicegateway.VoiceProviderSession, error) {
	return refusingSession{}, nil
}

type refusingSession struct{}

func (refusingSession) Start(context.Context, voiceproto.SessionStart, []voicegateway.ContinuationTurn) ([]voicegateway.ProviderOutbound, error) {
	return []voicegateway.ProviderOutbound{
		{Control: map[string]any{"type": voiceproto.TypeAITextDelta, "text": "ready"}},
		{Control: voiceproto.AITurnEnd{Type: voiceproto.TypeAITurnEnd}},
	}, nil
}

func (refusingSession) HandleClientControl(context.Context, string, []byte) ([]voicegateway.ProviderOutbound, error) {
	return nil, errors.New("upstream is gone")
}

func (refusingSession) HandleClientAudio(context.Context, []byte) ([]voicegateway.ProviderOutbound, error) {
	return nil, nil
}

func (refusingSession) SnapshotUtterances() []voicegateway.EndUtterance { return nil }
func (refusingSession) Close(context.Context) error                     { return nil }

func refusingRig(t *testing.T) *websocket.Conn {
	t.Helper()
	consumer := &stubConsumer{
		ticket: "good-ticket",
		out:    voicegateway.ConsumedTicket{TicketID: "t1", SessionID: "s1", UserID: "u1"},
	}
	h := voicegateway.NewHandler(consumer, &stubLifecycle{}, refusingProvider{}, nil,
		voicegateway.Options{InsecureSkipOrigin: true})
	mux := http.NewServeMux()
	h.Mount(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	t.Cleanup(cancel)
	conn := dialVoice(ctx, t, srv)
	t.Cleanup(func() { _ = conn.Close(websocket.StatusNormalClosure, "") })
	authAndStart(ctx, t, conn)
	return conn
}

// nextFrame reads one frame, or reports that the connection went quiet.
func nextFrame(ctx context.Context, conn *websocket.Conn, budget time.Duration) (map[string]any, bool) {
	readCtx, cancel := context.WithTimeout(ctx, budget)
	defer cancel()
	_, data, err := conn.Read(readCtx)
	if err != nil {
		return nil, false
	}
	var raw map[string]any
	if err := json.Unmarshal(data, &raw); err != nil {
		return nil, false
	}
	return raw, true
}

// The abort is the one frame whose provider failure is deliberately silent, and
// the reason is not aesthetic: an error frame here maps to iOS `.failed`, which
// kills the session the abort exists to keep alive.
func TestControlPolicy_TurnAbortFailureIsSilent(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	conn := refusingRig(t)

	if err := conn.Write(ctx, websocket.MessageText, voiceproto.MustMarshal(voiceproto.ClientTurnAbort{
		Type:    voiceproto.TypeClientTurnAbort,
		TurnID:  "turn-1",
		Outcome: voiceproto.ClientTurnAbortTimeout,
	})); err != nil {
		t.Fatalf("write abort: %v", err)
	}

	if frame, ok := nextFrame(ctx, conn, 300*time.Millisecond); ok {
		t.Fatalf("the abort answered with a frame (%v); it must be silent so the session survives", frame)
	}
}

// Every other forwarded frame announces. Same failing provider, same session —
// only the frame type differs, which is what makes this a statement about the
// policy table rather than about the provider.
func TestControlPolicy_InterruptFailureIsAnnounced(t *testing.T) {
	t.Parallel()
	ctx := context.Background()
	conn := refusingRig(t)

	if err := conn.Write(ctx, websocket.MessageText, voiceproto.MustMarshal(voiceproto.Interrupt{
		Type: voiceproto.TypeInterrupt,
	})); err != nil {
		t.Fatalf("write interrupt: %v", err)
	}

	frame, ok := nextFrame(ctx, conn, 2*time.Second)
	if !ok {
		t.Fatal("the interrupt failure was silent; it must tell the client")
	}
	if frame["type"] != voiceproto.TypeError {
		t.Fatalf("frame = %v, want an error frame", frame)
	}
	if frame["code"] != "provider_interrupt_failed" {
		t.Fatalf("code = %v, want provider_interrupt_failed", frame["code"])
	}
}

// The policy table's shape, asserted directly. This is the part that has to
// survive a refactor: a new frame type that forwards to the provider without a
// stated policy is a decision nobody made.
func TestControlPolicy_EveryForwardedFrameStatesItsFailurePolicy(t *testing.T) {
	policies := voicegateway.ProviderErrorPolicies()

	// Every frame the gateway forwards must appear.
	for _, frameType := range []string{
		voiceproto.TypeUserSpeechStart,
		voiceproto.TypeUserSpeechEnd,
		voiceproto.TypeInterrupt,
		voiceproto.TypeClientTurnAbort,
	} {
		policy, ok := policies[frameType]
		if !ok {
			t.Fatalf("%s forwards to the provider but has no failure policy", frameType)
		}
		// And each must have *chosen*: either a code to send, or a stated reason
		// for sending nothing.
		if policy.Code == "" && policy.SilentBecause == "" {
			t.Fatalf("%s says nothing about its failures", frameType)
		}
		if policy.Code != "" && policy.SilentBecause != "" {
			t.Fatalf("%s both announces (%q) and stays silent (%q)", frameType, policy.Code, policy.SilentBecause)
		}
	}

	// The one deviation from "announce when the user is waiting" is written down.
	abort := policies[voiceproto.TypeClientTurnAbort]
	if abort.Code != "" {
		t.Fatalf("the abort announces %q; it must stay silent", abort.Code)
	}
	if abort.SilentBecause == "" {
		t.Fatal("the abort is silent without saying why; the reason is the whole point")
	}
}
