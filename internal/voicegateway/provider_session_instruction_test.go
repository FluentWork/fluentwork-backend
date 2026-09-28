package voicegateway

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/coder/websocket"

	"github.com/FluentWork/fluentwork-backend/internal/voiceproto"
)

// startInstructionDuplexStub answers the handshake, lifts every frame it
// receives onto a channel, and answers the three events this test drives:
// session.update (with a transcription event ahead of its own acknowledgement),
// and the audio commit.
func startInstructionDuplexStub(t *testing.T) (string, <-chan map[string]any) {
	t.Helper()
	frames := make(chan map[string]any, 32)
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		write := func(raw string) {
			_ = conn.Write(context.Background(), websocket.MessageText, []byte(raw))
		}
		for {
			readCtx, cancel := context.WithTimeout(context.Background(), testBudget)
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
			switch frame["type"] {
			case "session.create":
				write(`{"type":"session.created","session":{"id":"stub-duplex"}}`)
			case "session.update":
				write(`{"type":"conversation.item.input_audio_transcription.completed","transcript":"how do I say 限流?"}`)
				write(`{"type":"session.updated"}`)
			case "input_audio_buffer.commit":
				write(`{"type":"response.output_text.delta","delta":"Rate limiting."}`)
				write(`{"type":"response.output_text.done","text":"Rate limiting."}`)
				write(`{"type":"response.done"}`)
			}
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http"), frames
}

func liveProviderSession(t *testing.T, endpoint string) *volcDuplexProviderSession {
	t.Helper()

	provider := NewVolcDuplexProvider(Config{
		VolcSpeechAPIKey:   "test-key",
		VolcDuplexEndpoint: endpoint,
		VolcDuplexModel:    "test-model",
		VolcDuplexVoice:    "test-voice",
		ClientAudioFormat:  "pcm-s16le",
	}, nil)

	ctx, cancel := context.WithTimeout(context.Background(), testBudget)
	t.Cleanup(cancel)

	sess, err := provider.Open(ctx, ConsumedTicket{TicketID: "t1", SessionID: "s1", UserID: "u1"}, &SeqAllocator{}, nil)
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close(context.Background()) })

	typed, ok := sess.(*volcDuplexProviderSession)
	if !ok {
		t.Fatalf("provider returned %T, want *volcDuplexProviderSession", sess)
	}
	if _, err := typed.Start(ctx, voiceproto.SessionStart{Type: voiceproto.TypeSessionStart}, SessionContext{}); err != nil {
		t.Fatalf("Start: %v", err)
	}
	return typed
}

// The whole PRD B1 delivery, at the boundary that actually talks to the vendor:
// the instruction reaches the model on the turn it was queued for, and the
// transcript the delivery drained still reaches the client.
//
// Both halves matter and they pull in opposite directions — injecting before
// the commit is what makes the instruction apply to this turn, and that same
// window is where the vendor streams the user's transcription.
func TestAQueuedInstructionReachesTheModelBeforeTheCommitAndKeepsWhatItDrained(t *testing.T) {
	t.Parallel()

	endpoint, frames := startInstructionDuplexStub(t)
	sess := liveProviderSession(t, endpoint)
	emitter := &recordingEmitter{}
	sess.SetOutboundEmitter(emitter.emit)
	sess.activeTurnID = "turn-1"

	if got := nextFrame(t, frames); got != "session.create" {
		t.Fatalf("first frame = %q, want session.create", got)
	}

	injector, ok := VoiceProviderSession(sess).(SessionInstructionInjector)
	if !ok {
		t.Fatal("the live provider cannot be told to close a session, so a mini session would never end on the real path")
	}
	injector.QueueSessionInstruction("这是本次练习的最后一轮。")

	if _, err := sess.HandleClientControl(context.Background(), voiceproto.TypeUserSpeechEnd,
		voiceproto.MustMarshal(voiceproto.UserSpeechEnd{Type: voiceproto.TypeUserSpeechEnd, Text: "how do I say 限流?"})); err != nil {
		t.Fatalf("HandleClientControl: %v", err)
	}

	if got := nextFrame(t, frames); got != "session.update" {
		t.Fatalf("first frame after a queued instruction = %q, want session.update before the endpoint that closes the turn", got)
	}
	if got := nextFrame(t, frames); got != "input_audio_buffer.commit" {
		t.Fatalf("second frame after a queued instruction = %q, want input_audio_buffer.commit", got)
	}

	got := emitter.asrTexts()
	if len(got) != 1 || got[0] != "how do I say 限流?" {
		t.Fatalf("client-visible ASR = %q, want the transcript the instruction's wait drained", got)
	}
	if texts := emitter.textDeltas(); len(texts) != 1 || texts[0] != "Rate limiting." {
		t.Fatalf("assistant text = %q, want the turn to still complete", texts)
	}
}

// An instruction is for the turn it was queued for. Replaying it on the next
// turn would tell a session that has already closed to close again.
func TestAnInstructionIsDeliveredOnce(t *testing.T) {
	t.Parallel()

	endpoint, frames := startInstructionDuplexStub(t)
	sess := liveProviderSession(t, endpoint)
	sess.SetOutboundEmitter((&recordingEmitter{}).emit)

	if got := nextFrame(t, frames); got != "session.create" {
		t.Fatalf("first frame = %q, want session.create", got)
	}
	sess.QueueSessionInstruction("last turn")

	if _, err := sess.HandleClientControl(context.Background(), voiceproto.TypeUserSpeechEnd, nil); err != nil {
		t.Fatalf("turn 1: HandleClientControl: %v", err)
	}
	if got := nextFrame(t, frames); got != "session.update" {
		t.Fatalf("turn 1 first frame = %q, want session.update", got)
	}
	if got := nextFrame(t, frames); got != "input_audio_buffer.commit" {
		t.Fatalf("turn 1 second frame = %q, want input_audio_buffer.commit", got)
	}
	if got := nextFrame(t, frames); got != "input_audio_mute.commit" {
		t.Fatalf("turn 1 third frame = %q, want input_audio_mute.commit", got)
	}

	if _, err := sess.HandleClientControl(context.Background(), voiceproto.TypeUserSpeechEnd, nil); err != nil {
		t.Fatalf("turn 2: HandleClientControl: %v", err)
	}
	if got := nextFrame(t, frames); got != "input_audio_buffer.commit" {
		t.Fatalf("turn 2 first frame = %q, want the turn to start with its commit — a replayed instruction would mean this session is being told to close twice", got)
	}
}
