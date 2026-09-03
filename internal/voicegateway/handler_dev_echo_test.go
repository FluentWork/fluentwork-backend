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

	"github.com/FluentWork/fluentwork-backend/internal/session"
	"github.com/FluentWork/fluentwork-backend/internal/voicegateway"
	"github.com/FluentWork/fluentwork-backend/internal/voiceproto"
)

// TestDevEcho_EndToEnd_FiresBadgeOnSeededCorpus is the canonical proof
// that the local B14 + B12 path works without Volcengine credentials.
//
// Pipeline under test:
//
//  1. iOS sends user.speech.end with text=nil (B14 default)
//  2. Gateway invokes DevEchoVoiceProvider.HandleClientControl
//  3. DevEchoVoiceProvider returns the configured ServerASRText + a
//     ClientASRTranscription control frame
//  4. Gateway scans outbound → finds non-empty ServerASRText → uses it
//     as the badge-detector input
//  5. BadgeEmitter runs HitDetector.Detect against the seeded corpus
//  6. On hit, BadgeEmitter writes a feedback.badge frame back over WSS
//
// If this test passes, a developer running:
//
//	VOICE_GATEWAY_PROVIDER=dev-echo \
//	VOICE_DEV_ECHO_TEXT="let's ship it today" \
//	./scripts/dev-up.sh
//	# optional: seed the app-server corpus for corpus UI; not needed for the badge path
//	./scripts/corpus-seed.sh
//
// in a Simulator can speak → see the B12 badge fire end-to-end in the
// iOS SpeakingRoom overlay.
func TestDevEcho_EndToEnd_FiresBadgeOnSeededCorpus(t *testing.T) {
	t.Parallel()

	// Anchor text MUST match a seeded phrase block's ExpressionEN OR
	// AnchorUserSaid closely enough for HitDetector's token-overlap scorer
	// to clear the 0.65 threshold. See voice_hit_detect.go::scoreCandidate.
	const echoText = "let's ship it today"

	consumer := &stubConsumer{
		ticket: "good-ticket",
		out: voicegateway.ConsumedTicket{
			TicketID:  "t1",
			SessionID: "s1",
			UserID:    "u1",
		},
	}
	life := &stubLifecycle{}

	src := newStubBlockSourceForHandlerTest(
		session.BlockCandidate{
			ID:             "block-ship-it",
			ExpressionEN:   "Let's ship it.",
			IntentZH:       "推动上线",
			AnchorUserSaid: "let's ship it",
		},
	)
	det := session.NewHitDetector(src)

	badgeEmitter := voicegateway.NewBadgeEmitterForTest(det, nil, voicegateway.BadgeEmitterOptions{
		Timeout:   500 * time.Millisecond,
		DedupeTTL: 5 * time.Second,
	})

	provider := voicegateway.NewDevEchoVoiceProvider(echoText, nil)

	h := voicegateway.NewHandler(consumer, life, provider, nil, voicegateway.Options{InsecureSkipOrigin: true})
	h.SetBadgeEmitter(badgeEmitter)
	mux := http.NewServeMux()
	h.Mount(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 8*time.Second)
	defer cancel()
	wsURL := "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/voice"
	conn, _, err := websocket.Dial(ctx, wsURL, nil)
	if err != nil {
		t.Fatalf("Dial: %v", err)
	}
	// No defer Close: the async badge goroutine needs the connection open
	// until it has written the badge frame. We close after reading below.

	if err := conn.Write(ctx, websocket.MessageText, voiceproto.MustMarshal(voiceproto.Auth{
		Type:   voiceproto.TypeAuth,
		Ticket: "good-ticket",
	})); err != nil {
		t.Fatalf("write auth: %v", err)
	}
	_ = readNextControlFrame(ctx, t, conn) // session.ready

	if err := conn.Write(ctx, websocket.MessageText, voiceproto.MustMarshal(voiceproto.SessionStart{
		Type: voiceproto.TypeSessionStart,
	})); err != nil {
		t.Fatalf("write session.start: %v", err)
	}
	_ = readNextControlFrame(ctx, t, conn) // ai.text.delta (bootstrap)
	_ = readNextControlFrame(ctx, t, conn) // ai.turn.end

	// B14 default: client sends text=nil. DevEchoVoiceProvider must
	// produce the authoritative ASR text.
	if err := conn.Write(ctx, websocket.MessageText, voiceproto.MustMarshal(voiceproto.UserSpeechEnd{
		Type:   voiceproto.TypeUserSpeechEnd,
		Text:   "",
		TurnID: "turn-e2e-1",
	})); err != nil {
		t.Fatalf("write user.speech.end: %v", err)
	}

	// Expect the relay frame first — proves dev-echo provider wired.
	relayFrame := readNextControlFrame(ctx, t, conn)
	if relayFrame["type"] != voiceproto.TypeClientASRTranscription {
		t.Fatalf("expected client.asr.transcription relay, got %#v", relayFrame)
	}
	if relayFrame["text"] != echoText {
		t.Fatalf("expected relay text %q, got %v", echoText, relayFrame["text"])
	}

	// The provider closes the turn with ai.turn.end and BadgeEmitter writes
	// feedback.badge asynchronously — order is not guaranteed. Drain until we
	// have both. readNextControlFrame's 2s per-read budget comfortably covers
	// the 500ms detector timeout configured above.
	var badgeFrame map[string]any
	sawTurnEnd := false
	for i := 0; i < 4 && (badgeFrame == nil || !sawTurnEnd); i++ {
		frame := readNextControlFrame(ctx, t, conn)
		switch frame["type"] {
		case voiceproto.TypeAITurnEnd:
			sawTurnEnd = true
			if got, _ := frame["turn_id"].(string); got != "turn-e2e-1" {
				t.Fatalf("expected ai.turn.end turn_id %q, got %q", "turn-e2e-1", got)
			}
		case voiceproto.TypeFeedbackBadge:
			badgeFrame = frame
		default:
			t.Fatalf("unexpected frame %#v after relay", frame)
		}
	}
	if badgeFrame == nil || !sawTurnEnd {
		t.Fatalf("expected both ai.turn.end and feedback.badge, got turnEnd=%v badge=%#v", sawTurnEnd, badgeFrame)
	}
	if badgeFrame["badge"] != "Let's ship it." {
		t.Fatalf("expected badge %q (from ExpressionEN), got %v", "Let's ship it.", badgeFrame["badge"])
	}
	if got, _ := badgeFrame["phrase_block_id"].(string); got != "block-ship-it" {
		t.Fatalf("expected phrase_block_id %q, got %v", "block-ship-it", got)
	}
	if got, _ := badgeFrame["tier"].(string); got != voiceproto.BadgeTierSoft {
		t.Fatalf("expected tier %q, got %v", voiceproto.BadgeTierSoft, got)
	}
	if got, _ := badgeFrame["session_id"].(string); got != "s1" {
		t.Fatalf("expected session_id %q, got %v", "s1", got)
	}
	if got, _ := badgeFrame["turn_id"].(string); got != "turn-e2e-1" {
		t.Fatalf("expected turn_id %q, got %v", "turn-e2e-1", got)
	}

	// Close cleanly now that we've read the badge frame.
	_ = conn.Close(websocket.StatusNormalClosure, "")
}

// TestDevEcho_NoBadgeWhenEchoTextEmpty guards the empty-echo-text
// configuration so a developer who forgets to set VOICE_DEV_ECHO_TEXT
// gets a no-op (logged warning at session open) instead of an error or
// a misleading badge fire.
func TestDevEcho_NoBadgeWhenEchoTextEmpty(t *testing.T) {
	t.Parallel()

	consumer := &stubConsumer{
		ticket: "good-ticket",
		out: voicegateway.ConsumedTicket{
			TicketID:  "t2",
			SessionID: "s2",
			UserID:    "u2",
		},
	}

	src := newStubBlockSourceForHandlerTest(
		session.BlockCandidate{ID: "block-x", ExpressionEN: "anything"},
	)
	det := session.NewHitDetector(src)
	badgeEmitter := voicegateway.NewBadgeEmitterForTest(det, nil, voicegateway.BadgeEmitterOptions{})
	if badgeEmitter == nil {
		t.Skip("BadgeEmitterForTest not available")
		return
	}

	provider := voicegateway.NewDevEchoVoiceProvider("", nil) // intentionally empty

	h := voicegateway.NewHandler(consumer, &stubLifecycle{}, provider, nil, voicegateway.Options{InsecureSkipOrigin: true})
	h.SetBadgeEmitter(badgeEmitter)
	mux := http.NewServeMux()
	h.Mount(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	ctx, cancel := context.WithTimeout(context.Background(), 4*time.Second)
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
	_ = readNextControlFrame(ctx, t, conn)
	if err := conn.Write(ctx, websocket.MessageText, voiceproto.MustMarshal(voiceproto.SessionStart{
		Type: voiceproto.TypeSessionStart,
	})); err != nil {
		t.Fatalf("write session.start: %v", err)
	}
	_ = readNextControlFrame(ctx, t, conn)
	_ = readNextControlFrame(ctx, t, conn)

	if err := conn.Write(ctx, websocket.MessageText, voiceproto.MustMarshal(voiceproto.UserSpeechEnd{
		Type:   voiceproto.TypeUserSpeechEnd,
		TurnID: "turn-empty-echo",
	})); err != nil {
		t.Fatalf("write user.speech.end: %v", err)
	}

	// DevEcho with empty EchoText returns no relay frame and no badge, but it
	// still closes the turn with ai.turn.end so the client state machine can
	// leave `.processing`. Assert the turn boundary arrives, then that no
	// relay/badge frame follows within a short quiet window.
	endFrame := readNextControlFrame(ctx, t, conn)
	if endFrame["type"] != voiceproto.TypeAITurnEnd {
		t.Fatalf("expected ai.turn.end, got %#v", endFrame)
	}
	if got, _ := endFrame["turn_id"].(string); got != "turn-empty-echo" {
		t.Fatalf("expected ai.turn.end turn_id %q, got %q", "turn-empty-echo", got)
	}
	quietCtx, cancelQuiet := context.WithTimeout(ctx, 300*time.Millisecond)
	defer cancelQuiet()
	if _, _, err := conn.Read(quietCtx); err == nil {
		t.Fatal("expected no relay/badge frame after empty dev-echo turn end, got one")
	}
}

// readNextControlFrame is the local helper that pulls the next text control frame
// off the WSS connection and decodes it as a generic JSON map. Frames
// not yet available fail the test fast — callers should know exactly
// how many frames to expect.
func readNextControlFrame(ctx context.Context, t *testing.T, conn *websocket.Conn) map[string]any {
	t.Helper()
	readCtx, cancel := context.WithTimeout(ctx, 2*time.Second)
	defer cancel()
	typ, data, err := conn.Read(readCtx)
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	if typ != websocket.MessageText {
		t.Fatalf("expected text frame, got %v", typ)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decode frame %q: %v", string(data), err)
	}
	return out
}
