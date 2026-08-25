package voicegateway_test

import (
	"context"
	"encoding/json"
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

type stubConsumer struct {
	ticket string
	out    voicegateway.ConsumedTicket
	err    error
	calls  int
}

func (s *stubConsumer) Consume(_ context.Context, rawTicket string) (voicegateway.ConsumedTicket, error) {
	s.calls++
	if s.err != nil {
		return voicegateway.ConsumedTicket{}, s.err
	}
	if rawTicket != s.ticket {
		return voicegateway.ConsumedTicket{}, errors.New("invalid ticket")
	}
	return s.out, nil
}

type stubLifecycle struct {
	activateCalls int
	endCalls      int
	lastEnd       voicegateway.EndSessionRequest
	activateErr   error
	endErr        error
}

func (s *stubLifecycle) Activate(_ context.Context, _ string) error {
	s.activateCalls++
	return s.activateErr
}

func (s *stubLifecycle) End(_ context.Context, req voicegateway.EndSessionRequest) error {
	s.endCalls++
	s.lastEnd = req
	return s.endErr
}

func TestVoiceHandshakeAndSessionLoop(t *testing.T) {
	t.Parallel()

	consumer := &stubConsumer{
		ticket: "good-ticket",
		out: voicegateway.ConsumedTicket{
			TicketID:  "t1",
			SessionID: "s1",
			UserID:    "u1",
		},
	}
	life := &stubLifecycle{}
	h := voicegateway.NewHandler(consumer, life, nil, voicegateway.Options{InsecureSkipOrigin: true})
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

	ready := readFrame(ctx, t, conn)
	if ready["type"] != voiceproto.TypeSessionReady {
		t.Fatalf("expected session.ready, got %#v", ready)
	}
	if ready["session_id"] != "s1" || ready["user_id"] != "u1" {
		t.Fatalf("unexpected ready payload: %#v", ready)
	}

	if err := conn.Write(ctx, websocket.MessageText, voiceproto.MustMarshal(voiceproto.SessionStart{
		Type:      voiceproto.TypeSessionStart,
		SceneType: "demo",
	})); err != nil {
		t.Fatalf("write session.start: %v", err)
	}
	delta := readFrame(ctx, t, conn)
	if delta["type"] != voiceproto.TypeAITextDelta {
		t.Fatalf("expected ai.text.delta, got %#v", delta)
	}

	if err := conn.Write(ctx, websocket.MessageText, voiceproto.MustMarshal(voiceproto.Ping{
		Type: voiceproto.TypePing,
		TS:   42,
	})); err != nil {
		t.Fatalf("write ping: %v", err)
	}
	pong := readFrame(ctx, t, conn)
	if pong["type"] != voiceproto.TypePong {
		t.Fatalf("expected pong, got %#v", pong)
	}

	if err := conn.Write(ctx, websocket.MessageText, voiceproto.MustMarshal(voiceproto.SessionEnd{
		Type:   voiceproto.TypeSessionEnd,
		Reason: "user",
	})); err != nil {
		t.Fatalf("write session.end: %v", err)
	}
	end := readFrame(ctx, t, conn)
	if end["type"] != voiceproto.TypeSessionEnd {
		t.Fatalf("expected session.end ack, got %#v", end)
	}

	if consumer.calls != 1 {
		t.Fatalf("consumer calls = %d", consumer.calls)
	}
	if life.activateCalls != 1 || life.endCalls != 1 {
		t.Fatalf("lifecycle activate=%d end=%d", life.activateCalls, life.endCalls)
	}
	if life.lastEnd.SessionID != "s1" || len(life.lastEnd.Utterances) != 1 || life.lastEnd.Utterances[0].Text != "ready" {
		t.Fatalf("unexpected end request: %+v", life.lastEnd)
	}
}

func TestVoiceHandshakeRejectsBadTicket(t *testing.T) {
	t.Parallel()

	consumer := &stubConsumer{err: errors.New("invalid ticket")}
	h := voicegateway.NewHandler(consumer, nil, nil, voicegateway.Options{InsecureSkipOrigin: true})
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
		Ticket: "bad",
	})); err != nil {
		t.Fatalf("write auth: %v", err)
	}

	frame := readFrame(ctx, t, conn)
	if frame["type"] != voiceproto.TypeError {
		t.Fatalf("expected error, got %#v", frame)
	}
}

func readFrame(ctx context.Context, t *testing.T, conn *websocket.Conn) map[string]any {
	t.Helper()
	typ, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("Read: %v", err)
	}
	if typ != websocket.MessageText {
		t.Fatalf("expected text frame, got %v", typ)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("unmarshal %s: %v", data, err)
	}
	return out
}
