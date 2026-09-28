package voicegateway_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coder/websocket"

	"github.com/FluentWork/fluentwork-backend/internal/voicegateway"
	"github.com/FluentWork/fluentwork-backend/internal/voiceproto"
)

func openSessionForTurnLimitTest(t *testing.T, life *stubLifecycle, providerSession *stubProviderSession) (context.Context, *websocket.Conn) {
	t.Helper()

	providerSession.turnEndOnSpeechEnd = true
	consumer := &stubConsumer{
		ticket: "good-ticket",
		out: voicegateway.ConsumedTicket{
			TicketID:  "t1",
			SessionID: "s-new",
			UserID:    "u1",
		},
	}
	h := voicegateway.NewHandler(consumer, life, &stubProvider{session: providerSession}, nil,
		voicegateway.Options{InsecureSkipOrigin: true})
	mux := http.NewServeMux()
	h.Mount(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), testBudget)
	t.Cleanup(cancel)

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/voice"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.Close(websocket.StatusNormalClosure, "") })

	if err := conn.Write(ctx, websocket.MessageText, voiceproto.MustMarshal(voiceproto.Auth{
		Type:   voiceproto.TypeAuth,
		Ticket: "good-ticket",
	})); err != nil {
		t.Fatalf("write auth: %v", err)
	}
	waitForType(ctx, t, conn, voiceproto.TypeSessionReady)

	if err := conn.Write(ctx, websocket.MessageText, voiceproto.MustMarshal(voiceproto.SessionStart{
		Type:      voiceproto.TypeSessionStart,
		SceneType: "demo",
	})); err != nil {
		t.Fatalf("write session.start: %v", err)
	}
	waitForType(ctx, t, conn, voiceproto.TypeAITurnEnd)
	return ctx, conn
}

// driveTurn runs one whole user turn and returns the ai.turn.end the provider
// answered it with. Waiting for that frame is what makes the assertions below
// race-free: the gateway queues any instruction for the turn before the
// provider is asked for its reply.
func driveTurn(ctx context.Context, t *testing.T, conn *websocket.Conn) map[string]any {
	t.Helper()
	if err := conn.Write(ctx, websocket.MessageText, voiceproto.MustMarshal(voiceproto.UserSpeechStart{
		Type: voiceproto.TypeUserSpeechStart,
	})); err != nil {
		t.Fatalf("write user.speech.start: %v", err)
	}
	if err := conn.Write(ctx, websocket.MessageText, voiceproto.MustMarshal(voiceproto.UserSpeechEnd{
		Type: voiceproto.TypeUserSpeechEnd,
		Text: "hello",
	})); err != nil {
		t.Fatalf("write user.speech.end: %v", err)
	}
	raw, ok := waitForType(ctx, t, conn, voiceproto.TypeAITurnEnd).(map[string]any)
	if !ok {
		t.Fatal("ai.turn.end did not decode to a JSON object")
	}
	return raw
}

func sessionComplete(frame map[string]any) bool {
	flag, _ := frame["session_complete"].(bool)
	return flag
}

// The whole feature end to end through the handler: app-server says how long
// the session is, the gateway counts user turns, and the last turn both tells
// the model to wrap up and tells the client the session is done.
func TestTheFinalTurnCarriesTheClosingInstructionAndTheTerminationSignal(t *testing.T) {
	t.Parallel()

	life := &stubLifecycle{turnLimit: 2}
	providerSession := &stubProviderSession{}
	ctx, conn := openSessionForTurnLimitTest(t, life, providerSession)

	first := driveTurn(ctx, t, conn)
	if len(providerSession.state().queuedInstructions) != 0 {
		t.Fatalf("turn 1 queued %v, want nothing before the limit", providerSession.state().queuedInstructions)
	}
	if sessionComplete(first) {
		t.Fatal("turn 1 said the session was complete, but it was only 1 of 2")
	}

	second := driveTurn(ctx, t, conn)
	if len(providerSession.state().queuedInstructions) != 1 {
		t.Fatalf("turn 2 queued %v, want exactly one closing instruction", providerSession.state().queuedInstructions)
	}
	if !sessionComplete(second) {
		t.Fatal("the last turn's ai.turn.end must carry session_complete")
	}
}

// The limit is whatever activation reported, not a number this code knows. An
// implementation that hardcoded five passes the test above and fails both of
// these.
func TestTheTurnLimitIsWhateverActivationReported(t *testing.T) {
	t.Parallel()

	for _, limit := range []int{1, 3} {
		t.Run("limit", func(t *testing.T) {
			t.Parallel()

			life := &stubLifecycle{turnLimit: limit}
			providerSession := &stubProviderSession{}
			ctx, conn := openSessionForTurnLimitTest(t, life, providerSession)

			reachedAt := 0
			for turn := 1; turn <= limit+1 && reachedAt == 0; turn++ {
				frame := driveTurn(ctx, t, conn)
				if len(providerSession.state().queuedInstructions) > 0 {
					reachedAt = turn
					if !sessionComplete(frame) {
						t.Fatalf("turn %d produced the closing instruction but its ai.turn.end was not stamped", turn)
					}
				}
			}
			if reachedAt != limit {
				t.Fatalf("the closing instruction arrived on turn %d, want turn %d", reachedAt, limit)
			}
			if len(providerSession.state().queuedInstructions) != 1 {
				t.Fatalf("queued %d instructions, want 1", len(providerSession.state().queuedInstructions))
			}
		})
	}
}

// A mini session is over its contract but the client may still be talking. The
// signal is a property of the session from then on, not a one-off event a
// dropped read could swallow — and the instruction is not re-sent per turn.
func TestTheSessionStaysCompleteAndIsNotClosedTwice(t *testing.T) {
	t.Parallel()

	life := &stubLifecycle{turnLimit: 1}
	providerSession := &stubProviderSession{}
	ctx, conn := openSessionForTurnLimitTest(t, life, providerSession)

	if frame := driveTurn(ctx, t, conn); !sessionComplete(frame) {
		t.Fatal("turn 1 was the session's only turn and must say so")
	}
	if frame := driveTurn(ctx, t, conn); !sessionComplete(frame) {
		t.Fatal("a session past its limit must keep saying so")
	}
	if queued := providerSession.state().queuedInstructions; len(queued) != 1 {
		t.Fatalf("queued %v, want the closing instruction exactly once", queued)
	}
}

// The reverse direction: a standard session has no length contract, so nothing
// may end it and nothing may be injected mid-session.
func TestAStandardSessionGetsNoClosingInstructionAndNoTerminationSignal(t *testing.T) {
	t.Parallel()

	life := &stubLifecycle{}
	providerSession := &stubProviderSession{}
	ctx, conn := openSessionForTurnLimitTest(t, life, providerSession)

	for turn := 1; turn <= 3; turn++ {
		if frame := driveTurn(ctx, t, conn); sessionComplete(frame) {
			t.Fatalf("turn %d of a standard session carried session_complete", turn)
		}
	}
	if queued := providerSession.state().queuedInstructions; len(queued) != 0 {
		t.Fatalf("a standard session was sent %v, want no instruction at all", queued)
	}
}
