package voicegateway_test

import (
	"context"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/FluentWork/fluentwork-backend/internal/voicegateway"
	"github.com/FluentWork/fluentwork-backend/internal/voiceproto"
)

func TestHandler_UnknownControlFrameIsIgnoredSessionStaysAlive(t *testing.T) {
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

	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"future.v3.frame","turn_id":"turn-1"}`)); err != nil {
		t.Fatalf("write unknown frame: %v", err)
	}
	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"type":"ai.tts.client.ack"}`)); err != nil {
		t.Fatalf("write second unknown frame: %v", err)
	}
	if err := conn.Write(ctx, websocket.MessageText, voiceproto.MustMarshal(voiceproto.Ping{
		Type: voiceproto.TypePing, TS: 11,
	})); err != nil {
		t.Fatalf("write ping: %v", err)
	}
	pong := readFrame(ctx, t, conn)
	if pong["type"] != voiceproto.TypePong {
		t.Fatalf("unknown type must not emit error; expected pong, got %#v", pong)
	}
	if pong["ts"] != float64(11) {
		t.Fatalf("pong ts = %#v", pong["ts"])
	}
	if len(providerSession.controlTypes) != 0 {
		t.Fatalf("unknown type must not reach the provider, got %#v", providerSession.controlTypes)
	}
}

func TestHandler_MalformedControlFrameStillInvalid(t *testing.T) {
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
	authAndStart(ctx, t, conn)

	if err := conn.Write(ctx, websocket.MessageText, []byte(`{not-json`)); err != nil {
		t.Fatalf("write malformed: %v", err)
	}
	errFrame := readFrame(ctx, t, conn)
	if errFrame["type"] != voiceproto.TypeError || errFrame["code"] != "invalid_frame" {
		t.Fatalf("malformed JSON must stay invalid_frame, got %#v", errFrame)
	}

	if err := conn.Write(ctx, websocket.MessageText, []byte(`{"turn_id":"turn-1"}`)); err != nil {
		t.Fatalf("write missing type: %v", err)
	}
	errFrame = readFrame(ctx, t, conn)
	if errFrame["type"] != voiceproto.TypeError || errFrame["code"] != "invalid_frame" {
		t.Fatalf("missing type must stay invalid_frame, got %#v", errFrame)
	}

	if err := conn.Write(ctx, websocket.MessageText, voiceproto.MustMarshal(voiceproto.Ping{
		Type: voiceproto.TypePing, TS: 3,
	})); err != nil {
		t.Fatalf("write ping: %v", err)
	}
	pong := readFrame(ctx, t, conn)
	if pong["type"] != voiceproto.TypePong {
		t.Fatalf("invalid_frame must not kill the session, got %#v", pong)
	}
}
