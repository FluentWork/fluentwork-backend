package voicepoc

import (
	"context"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// startDuplexMockServer stands up an httptest WSS server that runs `serve`
// in a goroutine for the duration of the test. The serve func receives the
// accepted *websocket.Conn and is responsible for driving one duplex
// session (responding to session.create, etc.). Tests can return a
// pre-canned sequence of DuplexEvent-equivalent JSON frames.
func startDuplexMockServer(t *testing.T, serve func(conn *websocket.Conn)) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
			InsecureSkipVerify: true,
		})
		if err != nil {
			t.Logf("mock server accept: %v", err)
			return
		}
		defer func() { _ = conn.CloseNow() }()
		serve(conn)
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

// writeJSONFrame sends one JSON text frame to the duplex mock client.
func writeJSONFrame(t *testing.T, conn *websocket.Conn, payload string) {
	t.Helper()
	if err := conn.Write(context.Background(), websocket.MessageText, []byte(payload)); err != nil {
		t.Fatalf("mock server write: %v", err)
	}
}

// TestCollectTurn_OutcomeTimeoutOnSilentWait covers docs/20 §1.2.a — the
// provider stays silent for the entire wait window, no events at all. The
// previous code path silently returned outcome=ok via logx.Segment; B15
// forces outcome=timeout and a non-nil error.
func TestCollectTurn_OutcomeTimeoutOnSilentWait(t *testing.T) {
	t.Parallel()

	url := startDuplexMockServer(t, func(conn *websocket.Conn) {
		// session.create → session.created
		readUntilType(t, conn, "session.create")
		writeJSONFrame(t, conn, `{"type":"session.created","session":{"id":"sess-timeout"}}`)
		// No further events; collectTurn must time out and report outcome=timeout.
		<-make(chan struct{})
	})

	session := openTestSession(t, url)
	defer func() { _ = session.Close(context.Background()) }()

	started := time.Now()
	turn, err := session.collectTurn(context.Background(), started, nil, 250*time.Millisecond)
	if err == nil {
		t.Fatalf("expected timeout error, got nil (turn=%+v)", turn)
	}
	if !strings.Contains(err.Error(), "timeout") {
		t.Fatalf("expected error to mention timeout, got %v", err)
	}
	if turn.Outcome != TurnOutcomeTimeout {
		t.Fatalf("expected Outcome=timeout, got %q (turn=%+v)", turn.Outcome, turn)
	}
	if turn.Transcript != "" || turn.AssistantText != "" {
		t.Fatalf("expected empty content on silent timeout, got transcript=%q assistant=%q",
			turn.Transcript, turn.AssistantText)
	}
}

// TestCollectTurn_OutcomePartialOnProgressThenWaitExpired covers the case
// where the wait window expires after some progress (e.g. ASR started but
// no response.done). Outcome must be partial; err may be nil so the caller
// still receives whatever content was salvaged.
func TestCollectTurn_OutcomePartialOnProgressThenWaitExpired(t *testing.T) {
	t.Parallel()

	url := startDuplexMockServer(t, func(conn *websocket.Conn) {
		readUntilType(t, conn, "session.create")
		writeJSONFrame(t, conn, `{"type":"session.created","session":{"id":"sess-partial"}}`)
		// Emit ASR started + delta + completed, but never response.done.
		writeJSONFrame(t, conn, `{"type":"conversation.item.input_audio_transcription.started"}`)
		writeJSONFrame(t, conn, `{"type":"conversation.item.input_audio_transcription.delta","transcript":"hello"}`)
		writeJSONFrame(t, conn, `{"type":"conversation.item.input_audio_transcription.completed","transcript":"hello there"}`)
		// No response.done → collectTurn should exit the loop on deadline.
		<-make(chan struct{})
	})

	session := openTestSession(t, url)
	defer func() { _ = session.Close(context.Background()) }()

	started := time.Now()
	turn, err := session.collectTurn(context.Background(), started, nil, 250*time.Millisecond)
	if err != nil {
		t.Fatalf("partial content should not surface as error, got %v", err)
	}
	if turn.Outcome != TurnOutcomePartial {
		t.Fatalf("expected Outcome=partial, got %q (turn=%+v)", turn.Outcome, turn)
	}
	if turn.Transcript != "hello there" {
		t.Fatalf("expected transcript to be salvaged, got %q", turn.Transcript)
	}
}

// TestCollectTurn_OutcomeErrorOnTransportFailureAfterProgress covers the shape
// seen in the 2026-09-10 physical-device log: the duplex dies mid-turn after
// delivering progress. The old code reported outcome=partial with a nil error,
// which hid a dead upstream behind a benign label — collectTurn returned in
// 2ms, which no wait window can produce. A read failure is not a timeout.
func TestCollectTurn_OutcomeErrorOnTransportFailureAfterProgress(t *testing.T) {
	t.Parallel()

	url := startDuplexMockServer(t, func(conn *websocket.Conn) {
		readUntilType(t, conn, "session.create")
		writeJSONFrame(t, conn, `{"type":"session.created","session":{"id":"sess-dead"}}`)
		writeJSONFrame(t, conn, `{"type":"conversation.item.input_audio_transcription.started"}`)
		writeJSONFrame(t, conn, `{"type":"conversation.item.input_audio_transcription.completed","transcript":"今天学习 clean architecture。"}`)
		writeJSONFrame(t, conn, `{"type":"response.output_text.delta","delta":"Sounds good."}`)
		// Upstream dies before response.done.
		_ = conn.CloseNow()
		<-make(chan struct{})
	})

	session := openTestSession(t, url)
	defer func() { _ = session.Close(context.Background()) }()

	started := time.Now()
	turn, err := session.collectTurn(context.Background(), started, nil, 5*time.Second)
	if err == nil {
		t.Fatalf("transport failure must surface an error, got nil (turn=%+v)", turn)
	}
	if errors.Is(err, context.DeadlineExceeded) {
		t.Fatalf("transport failure must not be classified as a timeout: %v", err)
	}
	if turn.Outcome != TurnOutcomeError {
		t.Fatalf("expected Outcome=error, got %q (turn=%+v)", turn.Outcome, turn)
	}
	// Content that arrived before the failure is still salvaged for the caller.
	if turn.Transcript == "" {
		t.Fatalf("expected transcript to be salvaged, got %+v", turn)
	}
	if elapsed := time.Since(started); elapsed >= 5*time.Second {
		t.Fatalf("collectTurn waited out the full window (%s) instead of failing fast", elapsed)
	}
}

// Volc streams the assistant's speech as base64 response.output_audio.delta
// events. coder/websocket defaults to a 32 KiB read limit, so one chatty reply
// broke the read and took the duplex with it. That is the failure behind the
// 2026-09-10 physical-device runs, where six of seven turns died with
// "message too big: read limited at 32769 bytes" and the session then lost its
// server-side conversation context on every reconnect.
func TestCollectTurn_ReadsAudioDeltaFramesPastTheDefaultReadLimit(t *testing.T) {
	t.Parallel()

	// ~3x the default 32 KiB limit; a real audio delta is bigger still.
	const bigDeltaBytes = 96 * 1024

	url := startDuplexMockServer(t, func(conn *websocket.Conn) {
		readUntilType(t, conn, "session.create")
		writeJSONFrame(t, conn, `{"type":"session.created","session":{"id":"sess-big"}}`)
		writeJSONFrame(t, conn, `{"type":"conversation.item.input_audio_transcription.started"}`)
		writeJSONFrame(t, conn, `{"type":"conversation.item.input_audio_transcription.completed","transcript":"hi"}`)
		writeJSONFrame(t, conn, `{"type":"response.output_text.delta","delta":"hello"}`)
		writeJSONFrame(t, conn,
			`{"type":"response.output_audio.delta","delta":"`+strings.Repeat("A", bigDeltaBytes)+`"}`)
		writeJSONFrame(t, conn, `{"type":"response.done"}`)
		<-make(chan struct{})
	})

	session := openTestSession(t, url)
	defer func() { _ = session.Close(context.Background()) }()

	started := time.Now()
	turn, err := session.collectTurn(context.Background(), started, nil, 5*time.Second)
	if err != nil {
		t.Fatalf("a large audio delta must not break the turn read: %v", err)
	}
	if turn.Outcome != TurnOutcomeOK {
		t.Fatalf("expected Outcome=ok, got %q (turn=%+v)", turn.Outcome, turn)
	}
}

// TestCollectTurn_OutcomeOKOnResponseDone is the happy path: response.done
// arrives before the wait window expires. Outcome must be ok and err nil.
func TestCollectTurn_OutcomeOKOnResponseDone(t *testing.T) {
	t.Parallel()

	url := startDuplexMockServer(t, func(conn *websocket.Conn) {
		readUntilType(t, conn, "session.create")
		writeJSONFrame(t, conn, `{"type":"session.created","session":{"id":"sess-ok"}}`)
		writeJSONFrame(t, conn, `{"type":"conversation.item.input_audio_transcription.started"}`)
		writeJSONFrame(t, conn, `{"type":"conversation.item.input_audio_transcription.completed","transcript":"hi"}`)
		writeJSONFrame(t, conn, `{"type":"response.output_text.delta","delta":"hello!"}`)
		writeJSONFrame(t, conn, `{"type":"response.done"}`)
		<-make(chan struct{})
	})

	session := openTestSession(t, url)
	defer func() { _ = session.Close(context.Background()) }()

	started := time.Now()
	turn, err := session.collectTurn(context.Background(), started, nil, 2*time.Second)
	if err != nil {
		t.Fatalf("ok path should not error, got %v", err)
	}
	if turn.Outcome != TurnOutcomeOK {
		t.Fatalf("expected Outcome=ok, got %q", turn.Outcome)
	}
	if turn.AssistantText != "hello!" {
		t.Fatalf("expected assistant text %q, got %q", "hello!", turn.AssistantText)
	}
}

// TestCollectTurn_OutcomeErrorOnProviderErrorEvent covers the case where
// the provider sends an error event. Outcome must be error and err non-nil.
func TestCollectTurn_OutcomeErrorOnProviderErrorEvent(t *testing.T) {
	t.Parallel()

	url := startDuplexMockServer(t, func(conn *websocket.Conn) {
		readUntilType(t, conn, "session.create")
		writeJSONFrame(t, conn, `{"type":"session.created","session":{"id":"sess-err"}}`)
		writeJSONFrame(t, conn, `{"type":"error","error":{"code":"server_overloaded","message":"busy"}}`)
		<-make(chan struct{})
	})

	session := openTestSession(t, url)
	defer func() { _ = session.Close(context.Background()) }()

	started := time.Now()
	turn, err := session.collectTurn(context.Background(), started, nil, 2*time.Second)
	if err == nil {
		t.Fatalf("expected error from provider error event, got nil (turn=%+v)", turn)
	}
	if turn.Outcome != TurnOutcomeError {
		t.Fatalf("expected Outcome=error, got %q", turn.Outcome)
	}
}

// readUntilType drains incoming text frames until one with the given type
// appears, or fails the test if the connection errors first. Used to
// synchronize the mock server on session.create.
func readUntilType(t *testing.T, conn *websocket.Conn, want string) {
	t.Helper()
	for {
		ctx, cancel := context.WithTimeout(context.Background(), 2*time.Second)
		_, data, err := conn.Read(ctx)
		cancel()
		if err != nil {
			t.Fatalf("mock server read: %v", err)
		}
		if strings.Contains(string(data), `"type":"`+want+`"`) {
			return
		}
	}
}

// openTestSession opens a DuplexSession against the mock WSS for use in
// collectTurn tests. Caller is responsible for closing.
func openTestSession(t *testing.T, wsURL string) *DuplexSession {
	t.Helper()
	s, err := OpenDuplex(context.Background(), DuplexConfig{
		APIKey:   "test-key-not-used-by-mock",
		Endpoint: wsURL,
		Model:    "test-model",
		Voice:    "test-voice",
	})
	if err != nil {
		t.Fatalf("OpenDuplex against mock: %v", err)
	}
	return s
}
