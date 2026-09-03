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

// TestDevEchoFixture_SendsAudioChunksAfterSpeechEnd is the T2 E2E proof
// that a pre-recorded PCM fixture streams back to iOS as binary audio frames
// after user.speech.end, enabling local audio path testing without a live
// Volcengine session.
//
// Pipeline under test:
//
//  1. iOS sends user.speech.end (text=nil, turn_id=turn-fixture-1)
//  2. DevEcho provider receives the frame and starts streaming the fixture PCM
//  3. Subsequent HandleClientAudio calls return remaining fixture chunks
//  4. When fixture is exhausted, provider sends ai.turn.end to unblock iOS
//
// This test uses a 640-byte fixture (1 × 20ms chunk at 16kHz mono PCM16).
func TestDevEchoFixture_SendsAudioChunksAfterSpeechEnd(t *testing.T) {
	t.Parallel()

	consumer := &stubConsumer{
		ticket: "good-ticket",
		out: voicegateway.ConsumedTicket{
			TicketID:  "t3",
			SessionID: "s3",
			UserID:    "u3",
		},
	}

	// Create a synthetic 640-byte fixture (1 chunk of 16kHz mono PCM16).
	// voicegateway.DevEchoFixtureGenerator produces a 1kHz sine wave.
	fixture := voicegateway.DevEchoFixtureGenerator(20) // 20ms = 640 bytes
	if len(fixture) != 640 {
		t.Fatalf("expected 640-byte fixture, got %d bytes", len(fixture))
	}

	provider := voicegateway.NewDevEchoVoiceProvider("test transcript", nil)

	h := voicegateway.NewHandler(consumer, &stubLifecycle{}, provider, nil, voicegateway.Options{InsecureSkipOrigin: true})
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
	_ = readNextControlFrame(ctx, t, conn) // ai.text.delta
	_ = readNextControlFrame(ctx, t, conn) // ai.turn.end

	// Send user.speech.end — this triggers the fixture to start streaming.
	if err := conn.Write(ctx, websocket.MessageText, voiceproto.MustMarshal(voiceproto.UserSpeechEnd{
		Type:   voiceproto.TypeUserSpeechEnd,
		TurnID: "turn-fixture-1",
	})); err != nil {
		t.Fatalf("write user.speech.end: %v", err)
	}

	// Expect relay frame (client.asr.transcription) first.
	relayFrame := readNextControlFrame(ctx, t, conn)
	if relayFrame["type"] != voiceproto.TypeClientASRTranscription {
		t.Fatalf("expected client.asr.transcription relay, got %#v", relayFrame)
	}

	// Next frame should be a binary audio chunk.
	binCtx, binCancel := context.WithTimeout(ctx, 1*time.Second)
	defer binCancel()
	typ, data, err := conn.Read(binCtx)
	if err != nil {
		t.Fatalf("read audio chunk: %v", err)
	}
	if typ != websocket.MessageBinary {
		t.Fatalf("expected binary frame, got %v", typ)
	}
	if len(data) != 640 {
		t.Fatalf("expected 640-byte audio chunk, got %d bytes", len(data))
	}

	// Exhaust the fixture: send enough binary frames to drain the remaining chunks.
	// Since we only have 1 chunk (20ms), sending another should trigger ai.turn.end.
	sendCtx, sendCancel := context.WithTimeout(ctx, 2*time.Second)
	defer sendCancel()
	// Send a dummy binary frame to trigger HandleClientAudio.
	if err := conn.Write(sendCtx, websocket.MessageBinary, []byte("dummy-audio")); err != nil {
		t.Fatalf("write dummy audio: %v", err)
	}

	// The next frame should be ai.turn.end (fixture exhausted).
	endFrame := readNextControlFrame(ctx, t, conn)
	if endFrame["type"] != voiceproto.TypeAITurnEnd {
		t.Fatalf("expected ai.turn.end after fixture exhausted, got %#v", endFrame)
	}
}

// TestDevEchoFixture_LargeFixture_SendsMultipleChunks verifies that multi-chunk
// fixtures stream correctly across multiple HandleClientAudio calls.
func TestDevEchoFixture_LargeFixture_SendsMultipleChunks(t *testing.T) {
	t.Parallel()

	consumer := &stubConsumer{
		ticket: "good-ticket",
		out: voicegateway.ConsumedTicket{
			TicketID:  "t4",
			SessionID: "s4",
			UserID:    "u4",
		},
	}

	// Create a 100ms fixture (5 chunks × 640 bytes = 3200 bytes).
	fixture := voicegateway.DevEchoFixtureGenerator(100)

	provider := voicegateway.NewDevEchoVoiceProvider("test", nil)

	h := voicegateway.NewHandler(consumer, &stubLifecycle{}, provider, nil, voicegateway.Options{InsecureSkipOrigin: true})
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
		TurnID: "turn-fixture-2",
	})); err != nil {
		t.Fatalf("write user.speech.end: %v", err)
	}

	// Read relay frame first.
	_ = readNextControlFrame(ctx, t, conn)

	// Read all binary chunks.
	const chunkSize = 640
	wantChunks := len(fixture) / chunkSize
	if len(fixture)%chunkSize != 0 {
		wantChunks++
	}
	gotChunks := 0
	for gotChunks < wantChunks {
		binCtx, binCancel := context.WithTimeout(ctx, 2*time.Second)
		typ, data, err := conn.Read(binCtx)
		binCancel()
		if err != nil {
			t.Fatalf("read chunk %d: %v", gotChunks+1, err)
		}
		if typ != websocket.MessageBinary {
			// ai.turn.end means we've read all chunks.
			if typ == websocket.MessageText {
				var frame map[string]any
				json.Unmarshal(data, &frame)
				if frame["type"] == voiceproto.TypeAITurnEnd {
					goto done
				}
			}
			t.Fatalf("expected binary frame or ai.turn.end at chunk %d, got type=%v", gotChunks+1, typ)
		}
		if len(data) != chunkSize && gotChunks < wantChunks-1 {
			t.Fatalf("expected %d-byte chunk, got %d bytes", chunkSize, len(data))
		}
		gotChunks++
	}

done:
	// After all chunks are read, the next text frame should be ai.turn.end.
	endFrame := readNextControlFrame(ctx, t, conn)
	if endFrame["type"] != voiceproto.TypeAITurnEnd {
		t.Fatalf("expected ai.turn.end after %d chunks, got %#v", gotChunks, endFrame)
	}
	if gotChunks != wantChunks {
		t.Fatalf("expected %d chunks, got %d", wantChunks, gotChunks)
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
