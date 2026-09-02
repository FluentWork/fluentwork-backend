package voicegateway_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/FluentWork/fluentwork-backend/internal/voicegateway"
	"github.com/FluentWork/fluentwork-backend/internal/voicepoc"
	"github.com/FluentWork/fluentwork-backend/internal/voiceproto"
)

// volcStubSession mirrors volcDuplexProviderSession's wire-level output
// without dialing the real Volcano API. It exists to verify the gateway's
// handler glue (provider → relay frame → badge emitter input) under the
// volc-duplex code path using only the public interface.
//
// turnResult is what the stub returns from every HandleClientControl call.
// Pass an empty transcript to simulate a turn where ASR returned nothing.
type volcStubSession struct {
	turnResult voicepoc.TurnResult
	calls      int
}

func (s *volcStubSession) Start(_ context.Context, _ voiceproto.SessionStart) ([]voicegateway.ProviderOutbound, error) {
	return []voicegateway.ProviderOutbound{
		{Control: map[string]any{"type": voiceproto.TypeAITextDelta, "text": "ready"}},
		{Control: voiceproto.AITurnEnd{Type: voiceproto.TypeAITurnEnd}},
	}, nil
}

func (s *volcStubSession) HandleClientControl(_ context.Context, _ string, _ []byte) ([]voicegateway.ProviderOutbound, error) {
	s.calls++
	out := make([]voicegateway.ProviderOutbound, 0, 2)
	if transcript := strings.TrimSpace(s.turnResult.Transcript); transcript != "" {
		out = append(out, voicegateway.ProviderOutbound{
			Control: voiceproto.ClientASRTranscription{
				Type:   voiceproto.TypeClientASRTranscription,
				Text:   transcript,
				TurnID: "volc-stub-turn",
			},
			ServerASRText: transcript, // B14: feeds badge detector
		})
	}
	if reply := strings.TrimSpace(s.turnResult.AssistantText); reply != "" {
		out = append(out, voicegateway.ProviderOutbound{
			Control: map[string]any{
				"type": voiceproto.TypeAITextDelta,
				"text": reply,
			},
		})
		out = append(out, voicegateway.ProviderOutbound{
			Control: voiceproto.AITurnEnd{Type: voiceproto.TypeAITurnEnd, TurnID: "volc-stub-turn"},
		})
	}
	return out, nil
}

func (s *volcStubSession) HandleClientAudio(_ context.Context, _ []byte) ([]voicegateway.ProviderOutbound, error) {
	return nil, nil
}

func (s *volcStubSession) SnapshotUtterances() []voicegateway.EndUtterance { return nil }
func (s *volcStubSession) Close(_ context.Context) error                   { return nil }

type volcStubProvider struct{ session *volcStubSession }

func (p *volcStubProvider) Open(_ context.Context, _ voicegateway.ConsumedTicket) (voicegateway.VoiceProviderSession, error) {
	return p.session, nil
}

// TestVolcDuplexProvider_RelayEmitsClientASRTranscription verifies that when
// a volc-duplex-flavored session produces a non-empty transcript the gateway
// forwards it to iOS as a client.asr.transcription frame. This is the live
// counterpart to the MockVoiceProvider relay test — it exercises the same
// handler glue but with a provider that *always* emits server ASR, which is
// the production reality.
func TestVolcDuplexProvider_RelayEmitsClientASRTranscription(t *testing.T) {
	t.Parallel()

	consumer := &stubConsumer{
		ticket: "good-ticket",
		out: voicegateway.ConsumedTicket{
			TicketID: "t1", SessionID: "s1", UserID: "u1",
		},
	}
	stubSession := &volcStubSession{
		turnResult: voicepoc.TurnResult{
			Transcript:    "we should ship it today",
			AssistantText: "Sounds good!",
		},
	}
	provider := &volcStubProvider{session: stubSession}

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

	// Auth
	if err := conn.Write(ctx, websocket.MessageText, voiceproto.MustMarshal(voiceproto.Auth{
		Type: voiceproto.TypeAuth, Ticket: "good-ticket",
	})); err != nil {
		t.Fatalf("write auth: %v", err)
	}
	_ = readFrame(ctx, t, conn) // session.ready

	// SessionStart → provider emits bootstrap ai.text.delta + ai.turn.end.
	if err := conn.Write(ctx, websocket.MessageText, voiceproto.MustMarshal(voiceproto.SessionStart{
		Type: voiceproto.TypeSessionStart,
	})); err != nil {
		t.Fatalf("write session.start: %v", err)
	}
	if frame := readFrame(ctx, t, conn); frame["type"] != voiceproto.TypeAITextDelta {
		t.Fatalf("expected ai.text.delta, got %#v", frame)
	}
	if frame := readFrame(ctx, t, conn); frame["type"] != voiceproto.TypeAITurnEnd {
		t.Fatalf("expected ai.turn.end, got %#v", frame)
	}

	// user.speech.end with EMPTY client text — volc-duplex supplies the ASR.
	if err := conn.Write(ctx, websocket.MessageText, voiceproto.MustMarshal(voiceproto.UserSpeechEnd{
		Type: voiceproto.TypeUserSpeechEnd,
		Text: "",
	})); err != nil {
		t.Fatalf("write user.speech.end: %v", err)
	}

	// Expect client.asr.transcription → ai.text.delta → ai.turn.end, in that order.
	relay := readFrame(ctx, t, conn)
	if relay["type"] != voiceproto.TypeClientASRTranscription {
		t.Fatalf("expected client.asr.transcription, got %#v", relay)
	}
	if relay["text"] != "we should ship it today" {
		t.Fatalf("relay text mismatch: got %v want %q", relay["text"], "we should ship it today")
	}

	reply := readFrame(ctx, t, conn)
	if reply["type"] != voiceproto.TypeAITextDelta {
		t.Fatalf("expected ai.text.delta after ASR, got %#v", reply)
	}
	if reply["text"] != "Sounds good!" {
		t.Fatalf("assistant reply mismatch: got %v want %q", reply["text"], "Sounds good!")
	}

	turnEnd := readFrame(ctx, t, conn)
	if turnEnd["type"] != voiceproto.TypeAITurnEnd {
		t.Fatalf("expected ai.turn.end after ASR, got %#v", turnEnd)
	}

	if stubSession.calls != 1 {
		t.Fatalf("HandleClientControl calls = %d, want 1", stubSession.calls)
	}
}

// TestVolcDuplexProvider_OmitsRelayWhenTranscriptEmpty mirrors the no-ASR
// fallback: when the live provider returns an empty transcript the gateway
// must not emit an empty client.asr.transcription frame (iOS would render a
// blank live transcript — bug).
func TestVolcDuplexProvider_OmitsRelayWhenTranscriptEmpty(t *testing.T) {
	t.Parallel()

	consumer := &stubConsumer{
		ticket: "good-ticket",
		out: voicegateway.ConsumedTicket{TicketID: "t1", SessionID: "s1", UserID: "u1"},
	}
	stubSession := &volcStubSession{
		turnResult: voicepoc.TurnResult{
			// Transcript intentionally empty: provider heard nothing.
			AssistantText: "I didn't catch that, could you repeat?",
		},
	}
	provider := &volcStubProvider{session: stubSession}

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
		Text: "",
	})); err != nil {
		t.Fatalf("write user.speech.end: %v", err)
	}

	// First frame MUST be ai.text.delta — never client.asr.transcription
	// when the transcript is empty (would otherwise push "" to iOS UI).
	first := readFrame(ctx, t, conn)
	if first["type"] == voiceproto.TypeClientASRTranscription {
		t.Fatalf("did not expect client.asr.transcription with empty transcript, got %#v", first)
	}
	if first["type"] != voiceproto.TypeAITextDelta {
		t.Fatalf("expected ai.text.delta, got %#v", first)
	}
}

// helper retained in package so we can decode raw frames in tests without
// duplicating boilerplate. Wraps json.Unmarshal so a single failing case
// fails the test with a readable message.
func decodeFrame(t *testing.T, raw []byte) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode frame %q: %v", raw, err)
	}
	return out
}
