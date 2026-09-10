package voicegateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/FluentWork/fluentwork-backend/internal/voiceproto"
)

// startRecordingDuplexStub answers the handshake and lifts every frame it
// receives onto a channel.
//
// A recording stub rather than an asserting one because the events under test
// get **no reply** — the vendor acknowledges none of them — so the only place
// the behaviour is visible is in what we sent.
func startRecordingDuplexStub(t *testing.T) (string, <-chan map[string]any) {
	t.Helper()
	frames := make(chan map[string]any, 32)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		for {
			readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_, data, readErr := conn.Read(readCtx)
			cancel()
			if readErr != nil {
				return
			}
			var frame map[string]any
			if json.Unmarshal(data, &frame) != nil {
				continue
			}
			select {
			case frames <- frame:
			default:
			}
			if frame["type"] == "session.create" {
				_ = conn.Write(context.Background(), websocket.MessageText,
					[]byte(`{"type":"session.created","session":{"id":"stub-duplex"}}`))
			}
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http"), frames
}

// nextFrame waits for the next recorded frame, failing the test rather than
// hanging if none arrives.
func nextFrame(t *testing.T, frames <-chan map[string]any) string {
	t.Helper()
	select {
	case frame := <-frames:
		kind, _ := frame["type"].(string)
		return kind
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for a frame")
		return ""
	}
}

func openMuteTestSession(t *testing.T) (*volcDuplexProviderSession, <-chan map[string]any) {
	t.Helper()

	endpoint, frames := startRecordingDuplexStub(t)
	provider := NewVolcDuplexProvider(Config{
		VolcSpeechAPIKey:   "test-key",
		VolcDuplexEndpoint: endpoint,
		VolcDuplexModel:    "test-model",
		VolcDuplexVoice:    "test-voice",
		ClientAudioFormat:  "pcm-s16le",
	}, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	t.Cleanup(cancel)

	sess, err := provider.Open(ctx, ConsumedTicket{TicketID: "t1", SessionID: "s1", UserID: "u1"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close(context.Background()) })

	if _, err := sess.Start(ctx, voiceproto.SessionStart{Type: voiceproto.TypeSessionStart}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	if got := nextFrame(t, frames); got != "session.create" {
		t.Fatalf("first frame = %q, want session.create", got)
	}
	typed, ok := sess.(*volcDuplexProviderSession)
	if !ok {
		t.Fatalf("provider returned %T, want *volcDuplexProviderSession", sess)
	}
	return typed, frames
}

// The client sends PCM only inside a speech window (docs/40), so every turn end
// leaves the vendor with an uplink that has gone quiet. The duplex model keeps
// its session alive on that stream, so without a mute declaration the vendor
// waits for input that will never come — which is the "server times out and the
// model stops responding" failure the vendor's own docs describe.
func TestVolcDuplexDeclaresMuteAtTurnEnd(t *testing.T) {
	t.Parallel()

	sess, frames := openMuteTestSession(t)

	if _, err := sess.HandleClientAudio(context.Background(), []byte{1, 2, 3, 4}); err != nil {
		t.Fatalf("HandleClientAudio: %v", err)
	}
	if got := nextFrame(t, frames); got != "input_audio_buffer.append" {
		t.Fatalf("frame after audio = %q, want input_audio_buffer.append", got)
	}

	// `user.speech.end` blocks collecting the turn, and the stub never answers.
	// The declarations are sent before that wait, which is the point: they must
	// not depend on the turn completing.
	shortCtx, cancel := context.WithTimeout(context.Background(), 700*time.Millisecond)
	defer cancel()
	_, _ = sess.HandleClientControl(shortCtx, voiceproto.TypeUserSpeechEnd, nil)

	if got := nextFrame(t, frames); got != "input_audio_buffer.commit" {
		t.Fatalf("frame after speech.end = %q, want input_audio_buffer.commit", got)
	}
	if got := nextFrame(t, frames); got != "input_audio_mute.commit" {
		t.Fatalf(
			"second frame after speech.end = %q, want input_audio_mute.commit — "+
				"buffering.commit force-ends the query, it does not declare the microphone silent",
			got,
		)
	}
}

// Resuming without unmuting leaves the vendor believing the microphone is still
// off, so the next turn's audio would arrive against a session that was told to
// expect nothing.
func TestVolcDuplexUnmutesBeforeTheNextTurnsAudio(t *testing.T) {
	t.Parallel()

	sess, frames := openMuteTestSession(t)

	// The muted state is set directly rather than by driving a turn end: that
	// path blocks in WaitTurnResult until the vendor answers, and cutting it
	// short with an expired context leaves the duplex unusable for the rest of
	// the test (a context cancellation is classified as a dead socket — see
	// docs/53 §4). TestVolcDuplexDeclaresMuteAtTurnEnd covers that this state is
	// reachable; this test is about what happens next.
	if _, err := sess.HandleClientAudio(context.Background(), []byte{1, 2, 3, 4}); err != nil {
		t.Fatalf("HandleClientAudio: %v", err)
	}
	nextFrame(t, frames) // append
	sess.inputMuted = true

	// Next turn: audio again.
	if _, err := sess.HandleClientAudio(context.Background(), []byte{5, 6, 7, 8}); err != nil {
		t.Fatalf("HandleClientAudio (second turn): %v", err)
	}
	if got := nextFrame(t, frames); got != "input_audio_unmute.commit" {
		t.Fatalf("frame at turn resume = %q, want input_audio_unmute.commit", got)
	}
	if got := nextFrame(t, frames); got != "input_audio_buffer.append" {
		t.Fatalf("frame after unmute = %q, want input_audio_buffer.append", got)
	}

	// And only once: a second chunk in the same turn must not unmute again.
	if _, err := sess.HandleClientAudio(context.Background(), []byte{9, 10}); err != nil {
		t.Fatalf("HandleClientAudio (same turn): %v", err)
	}
	if got := nextFrame(t, frames); got != "input_audio_buffer.append" {
		t.Fatalf("frame for the second chunk = %q, want a plain append — unmute belongs to the transition, not to every chunk", got)
	}
}

// A reset opens a fresh duplex that has been told nothing. Carrying the old
// flag over would skip the new session's unmute and leave its first turn
// arriving unannounced.
func TestVolcDuplexResetClearsMuteState(t *testing.T) {
	t.Parallel()

	sess, _ := openMuteTestSession(t)
	sess.inputMuted = true
	sess.resetDuplexFn = func(context.Context) error {
		sess.session = nil // stand-in for a replaced duplex
		return nil
	}

	if err := sess.resetDuplex(context.Background()); err != nil {
		t.Fatalf("resetDuplex: %v", err)
	}
	if sess.inputMuted {
		t.Fatal("mute state survived a duplex reset; the new session would never be unmuted")
	}
}
