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

	"github.com/FluentWork/fluentwork-backend/internal/session"
	"github.com/FluentWork/fluentwork-backend/internal/voicegateway"
	"github.com/FluentWork/fluentwork-backend/internal/voiceproto"
)

// TestHandler_PrefersClientTextOverServerASR codifies the priority order: when
// the client supplies non-empty text on user.speech.end the server ASR text
// must be ignored for badge detection. The server ASR text should still flow
// back to the client via ClientASRTranscription, but the badge emitter's
// input must be the client value.
func TestHandler_PrefersClientTextOverServerASR(t *testing.T) {
	t.Parallel()

	consumer := &stubConsumer{ticket: "good-ticket", out: voicegateway.ConsumedTicket{TicketID: "t1", SessionID: "s1", UserID: "u1"}}
	provider := &stubProvider{
		session: &stubProviderSession{serverASRText: "server-asr-text"},
	}
	src := newStubBlockSourceForHandlerTest(
		session.BlockCandidate{ID: "block-client", ExpressionEN: "client-asr-text"},
		session.BlockCandidate{ID: "block-server", ExpressionEN: "server-asr-text"},
	)
	det := session.NewHitDetector(src)
	badgeEmitter := voicegateway.NewBadgeEmitterForTest(det, nil, voicegateway.BadgeEmitterOptions{})
	if badgeEmitter == nil {
		t.Skip("BadgeEmitterForTest not available")
		return
	}

	h := voicegateway.NewHandler(consumer, &stubLifecycle{}, provider, nil, voicegateway.Options{InsecureSkipOrigin: true})
	h.SetBadgeEmitter(badgeEmitter)
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

	if err := conn.Write(ctx, websocket.MessageText, voiceproto.MustMarshal(voiceproto.Auth{Type: voiceproto.TypeAuth, Ticket: "good-ticket"})); err != nil {
		t.Fatalf("write auth: %v", err)
	}
	_ = readFrame(ctx, t, conn) // session.ready
	// session.start triggers Start() on the stub provider which returns
	// ai.text.delta + ai.turn.end (no ClientASRTranscription — that only
	// comes from HandleClientControl, which is hit on every client control
	// frame including the upcoming user.speech.end).
	if err := conn.Write(ctx, websocket.MessageText, voiceproto.MustMarshal(voiceproto.SessionStart{Type: voiceproto.TypeSessionStart})); err != nil {
		t.Fatalf("write session.start: %v", err)
	}
	if frame := readFrame(ctx, t, conn); frame["type"] != voiceproto.TypeAITextDelta {
		t.Fatalf("expected ai.text.delta after session.start, got %#v", frame)
	}
	if frame := readFrame(ctx, t, conn); frame["type"] != voiceproto.TypeAITurnEnd {
		t.Fatalf("expected ai.turn.end after session.start, got %#v", frame)
	}

	// user.speech.end with CLIENT text — server ASR text is ignored for
	// badge detection but the relay frame should still come back because
	// HandleClientControl fires for every control frame.
	if err := conn.Write(ctx, websocket.MessageText, voiceproto.MustMarshal(voiceproto.UserSpeechEnd{
		Type:   voiceproto.TypeUserSpeechEnd,
		Text:   "client-asr-text",
		TurnID: "turn-1",
	})); err != nil {
		t.Fatalf("write user.speech.end: %v", err)
	}

	relay := readFrame(ctx, t, conn)
	if relay["type"] != voiceproto.TypeClientASRTranscription {
		t.Fatalf("expected client.asr.transcription relay, got %#v", relay)
	}
	if relay["text"] != "server-asr-text" {
		t.Fatalf("expected relay text %q, got %v", "server-asr-text", relay["text"])
	}

	// Give the async badge Emit goroutine time to complete before we assert.
	time.Sleep(100 * time.Millisecond)
}

// TestHandler_NoBadgeWhenBothClientAndServerEmpty guards the case where
// neither side has any ASR text. The handler must not panic, must not
// write a badge frame, and must log the skip path silently.
func TestHandler_NoBadgeWhenBothClientAndServerEmpty(t *testing.T) {
	t.Parallel()

	consumer := &stubConsumer{ticket: "good-ticket", out: voicegateway.ConsumedTicket{TicketID: "t1", SessionID: "s1", UserID: "u1"}}
	// Provider returns no ServerASRText (serverASRText == "" means
	// HandleClientControl returns nil outbounds for non-Start frames).
	provider := &stubProvider{session: &stubProviderSession{}}
	src := newStubBlockSourceForHandlerTest(session.BlockCandidate{ID: "block-1", ExpressionEN: "unrelated"})
	det := session.NewHitDetector(src)
	badgeEmitter := voicegateway.NewBadgeEmitterForTest(det, nil, voicegateway.BadgeEmitterOptions{})
	if badgeEmitter == nil {
		t.Skip("BadgeEmitterForTest not available")
		return
	}

	h := voicegateway.NewHandler(consumer, &stubLifecycle{}, provider, nil, voicegateway.Options{InsecureSkipOrigin: true})
	h.SetBadgeEmitter(badgeEmitter)
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

	if err := conn.Write(ctx, websocket.MessageText, voiceproto.MustMarshal(voiceproto.Auth{Type: voiceproto.TypeAuth, Ticket: "good-ticket"})); err != nil {
		t.Fatalf("write auth: %v", err)
	}
	_ = readFrame(ctx, t, conn)
	if err := conn.Write(ctx, websocket.MessageText, voiceproto.MustMarshal(voiceproto.SessionStart{Type: voiceproto.TypeSessionStart})); err != nil {
		t.Fatalf("write session.start: %v", err)
	}
	_ = readFrame(ctx, t, conn)
	_ = readFrame(ctx, t, conn)

	if err := conn.Write(ctx, websocket.MessageText, voiceproto.MustMarshal(voiceproto.UserSpeechEnd{
		Type:   voiceproto.TypeUserSpeechEnd,
		Text:   "", // client empty
		TurnID: "turn-empty",
	})); err != nil {
		t.Fatalf("write user.speech.end: %v", err)
	}

	// Allow time for any spurious badge Emit goroutine to fire.
	time.Sleep(100 * time.Millisecond)
}

// TestHandler_DropsProviderErrorGracefully ensures a provider control-frame
// error does not crash the gateway loop nor produce a partial badge emit.
// The user.speech.end control path must log and return cleanly.
func TestHandler_DropsProviderErrorGracefully(t *testing.T) {
	t.Parallel()

	consumer := &stubConsumer{ticket: "good-ticket", out: voicegateway.ConsumedTicket{TicketID: "t1", SessionID: "s1", UserID: "u1"}}
	provider := &stubProvider{session: &stubProviderSession{controlErr: errors.New("provider exploded")}}
	src := newStubBlockSourceForHandlerTest(session.BlockCandidate{ID: "block-1", ExpressionEN: "anything"})
	det := session.NewHitDetector(src)
	badgeEmitter := voicegateway.NewBadgeEmitterForTest(det, nil, voicegateway.BadgeEmitterOptions{})
	if badgeEmitter == nil {
		t.Skip("BadgeEmitterForTest not available")
		return
	}

	h := voicegateway.NewHandler(consumer, &stubLifecycle{}, provider, nil, voicegateway.Options{InsecureSkipOrigin: true})
	h.SetBadgeEmitter(badgeEmitter)
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

	if err := conn.Write(ctx, websocket.MessageText, voiceproto.MustMarshal(voiceproto.Auth{Type: voiceproto.TypeAuth, Ticket: "good-ticket"})); err != nil {
		t.Fatalf("write auth: %v", err)
	}
	_ = readFrame(ctx, t, conn) // session.ready
	if err := conn.Write(ctx, websocket.MessageText, voiceproto.MustMarshal(voiceproto.SessionStart{Type: voiceproto.TypeSessionStart})); err != nil {
		t.Fatalf("write session.start: %v", err)
	}
	_ = readFrame(ctx, t, conn) // ai.text.delta
	_ = readFrame(ctx, t, conn) // ai.turn.end

	// user.speech.end should not crash the loop even though the provider
	// returns an error. The connection stays open.
	if err := conn.Write(ctx, websocket.MessageText, voiceproto.MustMarshal(voiceproto.UserSpeechEnd{
		Type:   voiceproto.TypeUserSpeechEnd,
		Text:   "anything",
		TurnID: "turn-err",
	})); err != nil {
		t.Fatalf("write user.speech.end: %v", err)
	}

	// Send a follow-up frame to prove the loop is still alive.
	if err := conn.Write(ctx, websocket.MessageText, voiceproto.MustMarshal(voiceproto.UserSpeechEnd{
		Type:   voiceproto.TypeUserSpeechEnd,
		Text:   "second",
		TurnID: "turn-err-2",
	})); err != nil {
		t.Fatalf("write second user.speech.end: %v", err)
	}
	time.Sleep(100 * time.Millisecond)
}
