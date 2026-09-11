package voicegateway_test

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/FluentWork/fluentwork-backend/internal/voicegateway"
	"github.com/FluentWork/fluentwork-backend/internal/voiceproto"
)

// startSessionOverWSS runs the handshake and sends `session.start` with the
// given payload, returning once the gateway has answered. Everything the test
// wants to assert on is read off the stubs afterwards.
func startSessionOverWSS(
	t *testing.T,
	life *stubLifecycle,
	providerSession *stubProviderSession,
	start voiceproto.SessionStart,
) {
	t.Helper()

	consumer := &stubConsumer{
		ticket: "good-ticket",
		out: voicegateway.ConsumedTicket{
			TicketID:  "t1",
			SessionID: "s-new",
			UserID:    "u1",
		},
	}
	provider := &stubProvider{session: providerSession}
	h := voicegateway.NewHandler(consumer, life, provider, nil, voicegateway.Options{InsecureSkipOrigin: true})
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
		Type:   voiceproto.TypeAuth,
		Ticket: "good-ticket",
	})); err != nil {
		t.Fatalf("write auth: %v", err)
	}
	if ready := readFrame(ctx, t, conn); ready["type"] != voiceproto.TypeSessionReady {
		t.Fatalf("expected session.ready, got %#v", ready)
	}

	if err := conn.Write(ctx, websocket.MessageText, voiceproto.MustMarshal(start)); err != nil {
		t.Fatalf("write session.start: %v", err)
	}
	// The gateway answers session.start with the provider's bootstrap frames.
	// Reading one is what makes the assertions below race-free: by the time a
	// frame is on the wire, Start has been called with its arguments.
	_ = readFrame(ctx, t, conn)
}

// The whole feature, end to end through the handler: the client names a
// previous session, the gateway resolves it, and the provider is handed the
// turns. Every link here is load-bearing — dropping any one of them leaves the
// session opening from zero with nothing in the logs saying why.
func TestSessionStartPassesContinuationContextToTheProvider(t *testing.T) {
	t.Parallel()

	life := &stubLifecycle{
		continuation: []voicegateway.ContinuationTurn{
			{Seq: 1, Speaker: "user", Text: "how do I say 限流?"},
			{Seq: 2, Speaker: "ai", Text: "Rate limiting."},
		},
	}
	providerSession := &stubProviderSession{}

	startSessionOverWSS(t, life, providerSession, voiceproto.SessionStart{
		Type:                  voiceproto.TypeSessionStart,
		SceneType:             "standup",
		ContinueFromSessionID: "s-previous",
	})

	if life.continuationCalls != 1 {
		t.Fatalf("ContinuationContext called %d times, want 1", life.continuationCalls)
	}
	// Both ids, and in this order. The current one is what the ownership check
	// compares against; swapping them would ask "may s-new read s-previous"
	// backwards and answer it wrongly.
	if life.continuationAsked != [2]string{"s-new", "s-previous"} {
		t.Fatalf("asked %v, want [s-new s-previous]", life.continuationAsked)
	}
	if life.continuationLimitIn != 0 {
		t.Fatalf("limit = %d, want 0 (app-server's default)", life.continuationLimitIn)
	}

	if len(providerSession.continuation) != 1 {
		t.Fatalf("Start called %d times, want 1", len(providerSession.continuation))
	}
	got := providerSession.continuation[0]
	if len(got) != 2 || got[0].Text != "how do I say 限流?" || got[1].Speaker != "ai" {
		t.Fatalf("provider got %+v", got)
	}
}

// A refusal is not fatal. Failing the whole session because a nice-to-have
// lookup missed would trade a small loss for a total one — and the user would
// see "can't start", not "no context".
func TestSessionStartOpensWithoutContextWhenResolutionFails(t *testing.T) {
	t.Parallel()

	life := &stubLifecycle{continuationErr: errors.New("NOT_FOUND")}
	providerSession := &stubProviderSession{}

	startSessionOverWSS(t, life, providerSession, voiceproto.SessionStart{
		Type:                  voiceproto.TypeSessionStart,
		ContinueFromSessionID: "someone-elses-session",
	})

	if life.continuationCalls != 1 {
		t.Fatalf("ContinuationContext called %d times, want 1", life.continuationCalls)
	}
	if len(providerSession.continuation) != 1 {
		t.Fatalf("Start called %d times, want 1", len(providerSession.continuation))
	}
	if len(providerSession.continuation[0]) != 0 {
		t.Fatalf("provider got %+v, want nothing", providerSession.continuation[0])
	}
}

// Nothing to look up means nothing is looked up. A session that did not ask
// for context must not pay for a round trip on its critical path.
func TestSessionStartWithoutContinuationIDDoesNotLookAnythingUp(t *testing.T) {
	t.Parallel()

	life := &stubLifecycle{continuation: []voicegateway.ContinuationTurn{{Seq: 1, Speaker: "user", Text: "x"}}}
	providerSession := &stubProviderSession{}

	startSessionOverWSS(t, life, providerSession, voiceproto.SessionStart{
		Type: voiceproto.TypeSessionStart,
	})

	if life.continuationCalls != 0 {
		t.Fatalf("ContinuationContext called %d times, want 0", life.continuationCalls)
	}
	if len(providerSession.continuation) != 1 || len(providerSession.continuation[0]) != 0 {
		t.Fatalf("provider got %+v, want nothing", providerSession.continuation)
	}
}
