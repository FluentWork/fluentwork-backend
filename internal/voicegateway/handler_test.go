package voicegateway_test

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/FluentWork/fluentwork-backend/internal/session"
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

type stubProvider struct {
	session *stubProviderSession
	err     error
	calls   int
}

func (s *stubProvider) Open(_ context.Context, _ voicegateway.ConsumedTicket) (voicegateway.VoiceProviderSession, error) {
	s.calls++
	if s.err != nil {
		return nil, s.err
	}
	if s.session == nil {
		s.session = &stubProviderSession{}
	}
	return s.session, nil
}

type stubProviderSession struct {
	startCalls   int
	controlTypes []string
	audioPayload [][]byte
	closed       bool
	utterances   []voicegateway.EndUtterance
	startErr     error
	controlErr   error
	audioErr     error
}

func (s *stubProviderSession) Start(_ context.Context, _ voiceproto.SessionStart) ([]voicegateway.ProviderOutbound, error) {
	s.startCalls++
	if s.startErr != nil {
		return nil, s.startErr
	}
	s.utterances = []voicegateway.EndUtterance{{Seq: 1, Speaker: "ai", Text: "provider-ready"}}
	return []voicegateway.ProviderOutbound{
		{
			Control: map[string]any{
				"type": voiceproto.TypeAITextDelta,
				"text": "provider-ready",
			},
		},
		{
			Control: voiceproto.AITurnEnd{
				Type: voiceproto.TypeAITurnEnd,
			},
		},
	}, nil
}

func (s *stubProviderSession) HandleClientControl(_ context.Context, frameType string, _ []byte) ([]voicegateway.ProviderOutbound, error) {
	s.controlTypes = append(s.controlTypes, frameType)
	return nil, s.controlErr
}

func (s *stubProviderSession) HandleClientAudio(_ context.Context, payload []byte) ([]voicegateway.ProviderOutbound, error) {
	s.audioPayload = append(s.audioPayload, append([]byte(nil), payload...))
	return nil, s.audioErr
}

func (s *stubProviderSession) SnapshotUtterances() []voicegateway.EndUtterance {
	return append([]voicegateway.EndUtterance(nil), s.utterances...)
}

func (s *stubProviderSession) Close(_ context.Context) error {
	s.closed = true
	return nil
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
	providerSession := &stubProviderSession{}
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
	if delta["text"] != "provider-ready" {
		t.Fatalf("unexpected provider delta: %#v", delta)
	}
	turnEnd := readFrame(ctx, t, conn)
	if turnEnd["type"] != voiceproto.TypeAITurnEnd {
		t.Fatalf("expected ai.turn.end, got %#v", turnEnd)
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
	if provider.calls != 1 || providerSession.startCalls != 1 {
		t.Fatalf("provider open=%d start=%d", provider.calls, providerSession.startCalls)
	}
	if life.lastEnd.SessionID != "s1" || len(life.lastEnd.Utterances) != 1 || life.lastEnd.Utterances[0].Text != "provider-ready" {
		t.Fatalf("unexpected end request: %+v", life.lastEnd)
	}
}

func TestVoiceHandshakeRejectsBadTicket(t *testing.T) {
	t.Parallel()

	consumer := &stubConsumer{err: errors.New("invalid ticket")}
	h := voicegateway.NewHandler(consumer, nil, nil, nil, voicegateway.Options{InsecureSkipOrigin: true})
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

func TestVoiceSessionStartFailsWhenProviderOpenFails(t *testing.T) {
	t.Parallel()

	consumer := &stubConsumer{
		ticket: "good-ticket",
		out: voicegateway.ConsumedTicket{
			TicketID:  "t1",
			SessionID: "s1",
			UserID:    "u1",
		},
	}
	provider := &stubProvider{err: errors.New("provider unavailable")}
	h := voicegateway.NewHandler(consumer, &stubLifecycle{}, provider, nil, voicegateway.Options{InsecureSkipOrigin: true})
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
	_ = readFrame(ctx, t, conn)

	if err := conn.Write(ctx, websocket.MessageText, voiceproto.MustMarshal(voiceproto.SessionStart{
		Type: voiceproto.TypeSessionStart,
	})); err != nil {
		t.Fatalf("write session.start: %v", err)
	}

	frame := readFrame(ctx, t, conn)
	if frame["code"] != "provider_open_failed" {
		t.Fatalf("expected provider_open_failed, got %#v", frame)
	}
}

func TestVoiceBinaryAudioReturnsProviderAudioFailure(t *testing.T) {
	t.Parallel()

	consumer := &stubConsumer{
		ticket: "good-ticket",
		out: voicegateway.ConsumedTicket{
			TicketID:  "t1",
			SessionID: "s1",
			UserID:    "u1",
		},
	}
	providerSession := &stubProviderSession{audioErr: errors.New("pcm required")}
	provider := &stubProvider{session: providerSession}
	h := voicegateway.NewHandler(consumer, &stubLifecycle{}, provider, nil, voicegateway.Options{InsecureSkipOrigin: true})
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
	_ = readFrame(ctx, t, conn)

	if err := conn.Write(ctx, websocket.MessageText, voiceproto.MustMarshal(voiceproto.SessionStart{
		Type: voiceproto.TypeSessionStart,
	})); err != nil {
		t.Fatalf("write session.start: %v", err)
	}
	_ = readFrame(ctx, t, conn)
	_ = readFrame(ctx, t, conn)

	if err := conn.Write(ctx, websocket.MessageBinary, []byte{1, 2, 3, 4}); err != nil {
		t.Fatalf("write binary audio: %v", err)
	}

	frame := readFrame(ctx, t, conn)
	if frame["code"] != "provider_audio_failed" {
		t.Fatalf("expected provider_audio_failed, got %#v", frame)
	}
	if frame["message"] != "pcm required" {
		t.Fatalf("unexpected message: %#v", frame)
	}
}

func TestVoiceSessionEndFiresBadgeOnHit(t *testing.T) {
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
	providerSession := &stubProviderSession{}
	provider := &stubProvider{session: providerSession}

	src := &integrationSource{candidates: []session.BlockCandidate{
		{ID: "block-1", ExpressionEN: "ship it", IntentZH: "推进"},
	}}
	det := session.NewHitDetector(src)

	var wg sync.WaitGroup
	emitter := voicegateway.NewBadgeEmitter(det, nil, voicegateway.BadgeEmitterOptions{
		Timeout: 500 * time.Millisecond,
	}, &wg)

	h := voicegateway.NewHandler(consumer, life, provider, nil, voicegateway.Options{InsecureSkipOrigin: true})
	h.SetBadgeEmitter(emitter)
	mux := http.NewServeMux()
	h.Mount(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/voice"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	// No defer Close here — the async badge goroutine needs the connection
	// to stay open until it has written the badge frame. We close it after
	// reading the badge below.

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
	_ = readFrame(ctx, t, conn)
	_ = readFrame(ctx, t, conn)

	if err := conn.Write(ctx, websocket.MessageText, voiceproto.MustMarshal(voiceproto.UserSpeechEnd{
		Type: voiceproto.TypeUserSpeechEnd,
		Text: "let's ship it today",
	})); err != nil {
		t.Fatalf("write user.speech.end: %v", err)
	}

	// Wait for the async badge goroutine to complete so the badge is definitely
	// written to the connection before we read it.
	wgDone := make(chan struct{})
	go func() {
		wg.Wait()
		close(wgDone)
	}()
	select {
	case <-wgDone:
		// Badge goroutine ran; now read the frame.
	case <-time.After(3 * time.Second):
		t.Fatalf("badge goroutine did not complete in time")
	}

	readCtx, readCancel := context.WithTimeout(ctx, 2*time.Second)
	defer readCancel()
	badgeFrame := waitForType(readCtx, t, conn, voiceproto.TypeFeedbackBadge)
	badge, ok := badgeFrame.(map[string]any)
	if !ok {
		t.Fatalf("expected object frame, got %#v", badgeFrame)
	}
	if badge["phrase_block_id"] != "block-1" {
		t.Fatalf("phrase_block_id: got %v", badge["phrase_block_id"])
	}
	if badge["dedupe_key"] != "s1|s1|block-1" {
		t.Fatalf("dedupe_key: got %v", badge["dedupe_key"])
	}

	if got := atomic.LoadInt32(&src.calls); got != 1 {
		t.Fatalf("source calls: got %d want 1", got)
	}
	// Close the connection after the badge goroutine has finished writing.
	_ = conn.Close(websocket.StatusNormalClosure, "")
}

func TestVoiceSessionEndEmitsNoBadgeOnMiss(t *testing.T) {
	t.Parallel()

	consumer := &stubConsumer{
		ticket: "good-ticket",
		out: voicegateway.ConsumedTicket{
			TicketID: "t1", SessionID: "s1", UserID: "u1",
		},
	}
	providerSession := &stubProviderSession{}
	provider := &stubProvider{session: providerSession}

	src := &integrationSource{candidates: []session.BlockCandidate{
		{ID: "block-1", ExpressionEN: "ship it"},
	}}
	emitter := voicegateway.NewBadgeEmitter(session.NewHitDetector(src), nil, voicegateway.BadgeEmitterOptions{
		Timeout: 500 * time.Millisecond,
	})
	h := voicegateway.NewHandler(consumer, &stubLifecycle{}, provider, nil, voicegateway.Options{InsecureSkipOrigin: true})
	h.SetBadgeEmitter(emitter)
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
	_ = readFrame(ctx, t, conn)
	_ = readFrame(ctx, t, conn)

	if err := conn.Write(ctx, websocket.MessageText, voiceproto.MustMarshal(voiceproto.UserSpeechEnd{
		Type: voiceproto.TypeUserSpeechEnd, Text: "totally unrelated content here",
	})); err != nil {
		t.Fatalf("write user.speech.end: %v", err)
	}

	readCtx, readCancel := context.WithTimeout(ctx, 600*time.Millisecond)
	defer readCancel()
	if _, _, err := conn.Read(readCtx); err == nil {
		t.Fatal("expected no badge frame on miss")
	} else if !errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("expected deadline exceeded, got %v", err)
	}
}

func TestVoiceSessionEndWithoutTextSkipsDetection(t *testing.T) {
	t.Parallel()

	consumer := &stubConsumer{
		ticket: "good-ticket",
		out: voicegateway.ConsumedTicket{
			TicketID: "t1", SessionID: "s1", UserID: "u1",
		},
	}
	providerSession := &stubProviderSession{}
	provider := &stubProvider{session: providerSession}

	src := &integrationSource{candidates: []session.BlockCandidate{
		{ID: "block-1", ExpressionEN: "ship it"},
	}}
	emitter := voicegateway.NewBadgeEmitter(session.NewHitDetector(src), nil, voicegateway.BadgeEmitterOptions{
		Timeout: 500 * time.Millisecond,
	})
	h := voicegateway.NewHandler(consumer, &stubLifecycle{}, provider, nil, voicegateway.Options{InsecureSkipOrigin: true})
	h.SetBadgeEmitter(emitter)
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
	_ = readFrame(ctx, t, conn)
	_ = readFrame(ctx, t, conn)

	if err := conn.Write(ctx, websocket.MessageText, voiceproto.MustMarshal(voiceproto.UserSpeechEnd{
		Type: voiceproto.TypeUserSpeechEnd, // Text intentionally omitted.
	})); err != nil {
		t.Fatalf("write user.speech.end: %v", err)
	}

	if got := atomic.LoadInt32(&src.calls); got != 0 {
		t.Fatalf("source calls when text empty: got %d want 0", got)
	}
}

// integrationSource is a session.BlockSource with an atomic counter and
// snapshot-on-call semantics, used by the handler-level integration tests.
type integrationSource struct {
	candidates []session.BlockCandidate
	calls      int32
}

func (s *integrationSource) CandidatesForUser(ctx context.Context, _ string) ([]session.BlockCandidate, error) {
	atomic.AddInt32(&s.calls, 1)
	if err := ctx.Err(); err != nil {
		return nil, err
	}
	out := make([]session.BlockCandidate, len(s.candidates))
	copy(out, s.candidates)
	return out, nil
}

// waitForType reads frames until one with the requested type arrives or the
// context expires.
func waitForType(ctx context.Context, t *testing.T, conn *websocket.Conn, want string) any {
	t.Helper()
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("waitForType(%s): %v", want, err)
		}
		var raw map[string]any
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Fatalf("decode frame: %v", err)
		}
		if raw["type"] == want {
			return raw
		}
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
