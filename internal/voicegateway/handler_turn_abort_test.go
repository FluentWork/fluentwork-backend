package voicegateway_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/FluentWork/fluentwork-backend/internal/voicegateway"
	"github.com/FluentWork/fluentwork-backend/internal/voiceproto"
)

func TestHandler_ClientTurnAbortKeepsSessionAlive(t *testing.T) {
	t.Parallel()

	consumer := &stubConsumer{
		ticket: "good-ticket",
		out:    voicegateway.ConsumedTicket{TicketID: "t1", SessionID: "s1", UserID: "u1"},
	}
	providerSession := &stubProviderSession{}
	h := voicegateway.NewHandler(consumer, &stubLifecycle{}, &stubProvider{session: providerSession}, nil, voicegateway.Options{InsecureSkipOrigin: true})
	mux := http.NewServeMux()
	h.Mount(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn := dialVoice(ctx, t, srv)
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()
	authAndStart(ctx, t, conn)

	if err := conn.Write(ctx, websocket.MessageText, voiceproto.MustMarshal(voiceproto.UserSpeechStart{
		Type: voiceproto.TypeUserSpeechStart,
	})); err != nil {
		t.Fatalf("write user.speech.start: %v", err)
	}
	if err := conn.Write(ctx, websocket.MessageText, voiceproto.MustMarshal(voiceproto.ClientTurnAbort{
		Type:    voiceproto.TypeClientTurnAbort,
		TurnID:  "turn-1",
		Outcome: voiceproto.ClientTurnAbortTimeout,
	})); err != nil {
		t.Fatalf("write client.turn.abort: %v", err)
	}

	if err := conn.Write(ctx, websocket.MessageText, voiceproto.MustMarshal(voiceproto.Ping{
		Type: voiceproto.TypePing, TS: 7,
	})); err != nil {
		t.Fatalf("write ping: %v", err)
	}
	pong := readFrame(ctx, t, conn)
	if pong["type"] != voiceproto.TypePong {
		t.Fatalf("expected pong (session alive, no error/ai.turn.end), got %#v", pong)
	}

	if got := providerSession.controlTypes; len(got) != 2 ||
		got[0] != voiceproto.TypeUserSpeechStart ||
		got[1] != voiceproto.TypeClientTurnAbort {
		t.Fatalf("provider control types = %#v want [user.speech.start client.turn.abort]", got)
	}
}

func TestHandler_ClientTurnAbortBeforeStartIsNoop(t *testing.T) {
	t.Parallel()

	consumer := &stubConsumer{
		ticket: "good-ticket",
		out:    voicegateway.ConsumedTicket{TicketID: "t1", SessionID: "s1", UserID: "u1"},
	}
	h := voicegateway.NewHandler(consumer, &stubLifecycle{}, &stubProvider{session: &stubProviderSession{}}, nil, voicegateway.Options{InsecureSkipOrigin: true})
	mux := http.NewServeMux()
	h.Mount(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn := dialVoice(ctx, t, srv)
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()

	if err := conn.Write(ctx, websocket.MessageText, voiceproto.MustMarshal(voiceproto.Auth{
		Type: voiceproto.TypeAuth, Ticket: "good-ticket",
	})); err != nil {
		t.Fatalf("write auth: %v", err)
	}
	_ = readFrame(ctx, t, conn)

	if err := conn.Write(ctx, websocket.MessageText, voiceproto.MustMarshal(voiceproto.ClientTurnAbort{
		Type:    voiceproto.TypeClientTurnAbort,
		Outcome: voiceproto.ClientTurnAbortUserAbandoned,
	})); err != nil {
		t.Fatalf("write abort: %v", err)
	}

	if err := conn.Write(ctx, websocket.MessageText, voiceproto.MustMarshal(voiceproto.Ping{
		Type: voiceproto.TypePing, TS: 1,
	})); err != nil {
		t.Fatalf("write ping: %v", err)
	}
	pong := readFrame(ctx, t, conn)
	if pong["type"] != voiceproto.TypePong {
		t.Fatalf("abort before session.start must be a silent no-op, got %#v", pong)
	}
}

func TestHandler_ClientTurnAbortRejectsOkOutcome(t *testing.T) {
	t.Parallel()

	consumer := &stubConsumer{
		ticket: "good-ticket",
		out:    voicegateway.ConsumedTicket{TicketID: "t1", SessionID: "s1", UserID: "u1"},
	}
	providerSession := &stubProviderSession{}
	h := voicegateway.NewHandler(consumer, &stubLifecycle{}, &stubProvider{session: providerSession}, nil, voicegateway.Options{InsecureSkipOrigin: true})
	mux := http.NewServeMux()
	h.Mount(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	conn := dialVoice(ctx, t, srv)
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()
	authAndStart(ctx, t, conn)

	if err := conn.Write(ctx, websocket.MessageText, voiceproto.MustMarshal(voiceproto.ClientTurnAbort{
		Type:    voiceproto.TypeClientTurnAbort,
		TurnID:  "turn-1",
		Outcome: "ok",
	})); err != nil {
		t.Fatalf("write abort: %v", err)
	}
	errFrame := readFrame(ctx, t, conn)
	if errFrame["type"] != voiceproto.TypeError || errFrame["code"] != "invalid_frame" {
		t.Fatalf("expected invalid_frame, got %#v", errFrame)
	}
	if len(providerSession.controlTypes) != 0 {
		t.Fatalf("invalid abort must not reach the provider, got %#v", providerSession.controlTypes)
	}
}

func dialVoice(ctx context.Context, t *testing.T, srv *httptest.Server) *websocket.Conn {
	t.Helper()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/voice"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	return conn
}

func authAndStart(ctx context.Context, t *testing.T, conn *websocket.Conn) {
	t.Helper()
	if err := conn.Write(ctx, websocket.MessageText, voiceproto.MustMarshal(voiceproto.Auth{
		Type: voiceproto.TypeAuth, Ticket: "good-ticket",
	})); err != nil {
		t.Fatalf("write auth: %v", err)
	}
	_ = readFrame(ctx, t, conn)
	if err := conn.Write(ctx, websocket.MessageText, voiceproto.MustMarshal(voiceproto.SessionStart{
		Type: voiceproto.TypeSessionStart,
	})); err != nil {
		t.Fatalf("write session.start: %v", err)
	}
	_ = readFrame(ctx, t, conn) // ai.text.delta
	_ = readFrame(ctx, t, conn) // bootstrap ai.turn.end
}
