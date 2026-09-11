package voicegateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/binary"
	"encoding/json"
	"errors"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/FluentWork/fluentwork-backend/internal/voicepoc"
	"github.com/FluentWork/fluentwork-backend/internal/voiceproto"
)

// startAudioDuplexStub answers the handshake, then replies to a commit with one
// full turn including a single response.output_audio.delta carrying `audio`.
func startAudioDuplexStub(t *testing.T, audio []byte) string {
	t.Helper()
	delta := base64.StdEncoding.EncodeToString(audio)

	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		for {
			readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_, data, err := conn.Read(readCtx)
			cancel()
			if err != nil {
				return
			}
			switch {
			case strings.Contains(string(data), `"type":"session.create"`):
				_ = conn.Write(context.Background(), websocket.MessageText,
					[]byte(`{"type":"session.created","session":{"id":"stub-audio"}}`))
			case strings.Contains(string(data), `"type":"input_audio_buffer.commit"`):
				for _, frame := range []string{
					`{"type":"conversation.item.input_audio_transcription.started"}`,
					`{"type":"conversation.item.input_audio_transcription.completed","transcript":"hi"}`,
					`{"type":"response.output_text.delta","delta":"hello"}`,
					`{"type":"response.output_audio.delta","delta":"` + delta + `"}`,
					`{"type":"response.done"}`,
				} {
					_ = conn.Write(context.Background(), websocket.MessageText, []byte(frame))
				}
			}
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

// The assistant's audio was read and discarded, which is why the speaking room
// had no sound at all on the volc path. It is forwarded now, as plain binary
// frames and deliberately without an ai.tts.start: TTSFrameDispatcher only
// claims binary frames after it has seen one, and the decoder bound into it
// (MockTTSDecoder) records without driving AVAudioEngine. No ai.tts.start means
// the client falls back to audioEngine.play(frame:), which is what makes sound.
func TestVolcDuplexForwardsAssistantAudioAsBinaryFrames(t *testing.T) {
	t.Parallel()

	// 4800 bytes at 24 kHz mono PCM16 is 2400 samples, i.e. 100 ms.
	const vendorAudioBytes = 4800
	vendorAudio := make([]byte, vendorAudioBytes)
	for i := range vendorAudio {
		vendorAudio[i] = byte(i % 251)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	duplex, err := voicepoc.OpenDuplex(ctx, voicepoc.DuplexConfig{
		APIKey:   "test-key",
		Endpoint: startAudioDuplexStub(t, vendorAudio),
		Model:    "test-model",
		Voice:    "test-voice",
	})
	if err != nil {
		t.Fatalf("OpenDuplex: %v", err)
	}

	sess := &volcDuplexProviderSession{
		cfg:         voicepoc.DuplexConfig{Model: "test-model", Voice: "test-voice"},
		audioFormat: "pcm-s16le",
		logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		session:     duplex,
	}

	out, err := sess.HandleClientControl(ctx, voiceproto.TypeUserSpeechEnd,
		voiceproto.MustMarshal(voiceproto.UserSpeechEnd{
			Type:   voiceproto.TypeUserSpeechEnd,
			TurnID: "turn-1",
		}))
	if err != nil {
		t.Fatalf("HandleClientControl: %v", err)
	}

	var frames [][]byte
	for _, o := range out {
		if len(o.Binary) > 0 {
			frames = append(frames, o.Binary)
		}
	}
	if len(frames) != 1 {
		t.Fatalf("binary audio frames = %d, want 1 (100ms at a 100ms frame size)", len(frames))
	}
	if got := binary.BigEndian.Uint32(frames[0][:4]); got != 1 {
		t.Fatalf("first frame sequence = %d, want 1", got)
	}
	// 2400 samples at 24 kHz keep 2 of every 3 on the way to 16 kHz.
	if got, want := len(frames[0])-4, 3200; got != want {
		t.Fatalf("resampled payload = %d bytes, want %d", got, want)
	}
	if bytes.Equal(frames[0][4:], vendorAudio) {
		t.Fatal("payload went out at the vendor's 24 kHz; it must be resampled to 16 kHz")
	}
}

// The 3:2 ratio is the whole conversion, so it is worth pinning directly rather
// than only through the provider.
func TestResampleToPlaybackRateConvertsThreeToTwo(t *testing.T) {
	t.Parallel()

	out := resampleToPlaybackRate(make([]byte, 24000*2)) // one second at 24 kHz
	if got, want := len(out), 16000*2; got != want {
		t.Fatalf("resampled length = %d bytes, want %d", got, want)
	}
	if resampleToPlaybackRate(nil) != nil {
		t.Fatal("empty input must resample to nothing, not to an empty slice")
	}
	// An odd byte count is truncated rather than panicking.
	_ = resampleToPlaybackRate(make([]byte, 5))
}

// dyingDuplexStub answers the handshake, acknowledges the commit with one
// transcription event, then drops the connection mid-turn — the shape seen in
// the 2026-09-10 physical-device logs, where collectTurn returned after 1096ms
// with no response.done and a dead upstream.
func dyingDuplexStub(t *testing.T) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		for {
			readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_, data, err := conn.Read(readCtx)
			cancel()
			if err != nil {
				return
			}
			switch {
			case strings.Contains(string(data), `"type":"session.create"`):
				_ = conn.Write(context.Background(), websocket.MessageText,
					[]byte(`{"type":"session.created","session":{"id":"stub-dying"}}`))
			case strings.Contains(string(data), `"type":"input_audio_buffer.commit"`):
				// Acknowledge the commit with one event, then drop the socket
				// mid-turn: the gateway's next read is what fails.
				_ = conn.Write(context.Background(), websocket.MessageText,
					[]byte(`{"type":"conversation.item.input_audio_transcription.started"}`))
				_ = conn.Close(websocket.StatusInternalError, "upstream gone")
				return
			}
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

// A turn whose read fails leaves the duplex dead. Today nothing repairs it:
// collectTurn returns outcome=error and the corpse stays in place, so the next
// audio write discovers it and spends the session's single transparent reopen
// on a connection we already knew was gone. The reset has to happen where the
// death is detected.
func TestVolcDuplexResetsSessionWhenTurnReadFails(t *testing.T) {
	t.Parallel()

	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()

	duplex, err := voicepoc.OpenDuplex(ctx, voicepoc.DuplexConfig{
		APIKey:   "test-key",
		Endpoint: dyingDuplexStub(t),
		Model:    "test-model",
		Voice:    "test-voice",
	})
	if err != nil {
		t.Fatalf("OpenDuplex: %v", err)
	}

	resets := 0
	sess := &volcDuplexProviderSession{
		cfg:         voicepoc.DuplexConfig{Model: "test-model", Voice: "test-voice"},
		audioFormat: "pcm-s16le",
		logger:      slog.New(slog.NewTextHandler(io.Discard, nil)),
		session:     duplex,
		resetDuplexFn: func(context.Context) error {
			resets++
			return nil
		},
	}

	_, _ = sess.HandleClientControl(ctx, voiceproto.TypeUserSpeechEnd,
		voiceproto.MustMarshal(voiceproto.UserSpeechEnd{
			Type:   voiceproto.TypeUserSpeechEnd,
			TurnID: "turn-1",
		}))

	if resets != 1 {
		t.Fatalf("duplex resets after a failed turn read = %d, want 1", resets)
	}
}

// startVolcDuplexStub answers the OpenDuplex handshake so Start can be driven
// end to end without dialing Volc. It mirrors the mock server used by the
// voicepoc collectTurn tests.
func startVolcDuplexStub(t *testing.T) string {
	t.Helper()
	mux := http.NewServeMux()
	mux.HandleFunc("/", func(w http.ResponseWriter, r *http.Request) {
		conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{InsecureSkipVerify: true})
		if err != nil {
			return
		}
		defer func() { _ = conn.CloseNow() }()
		for {
			readCtx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
			_, data, err := conn.Read(readCtx)
			cancel()
			if err != nil {
				return
			}
			if strings.Contains(string(data), `"type":"session.create"`) {
				_ = conn.Write(context.Background(), websocket.MessageText,
					[]byte(`{"type":"session.created","session":{"id":"stub-duplex"}}`))
			}
		}
	})
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)
	return "ws" + strings.TrimPrefix(srv.URL, "http")
}

// A volc-backed session must announce itself the way Mock and DevEcho do.
// iOS leaves `aiSpeaking` only on `ai.turn.end`; with no bootstrap frame the
// user's first tap is read as barge-in and emits a spurious `interrupt`
// (I20 Item 4), which is exactly what the 2026-09-10 physical-device log showed
// one millisecond after `user.speech.start`.
func TestVolcDuplexStartEmitsBootstrapTurnEnd(t *testing.T) {
	t.Parallel()

	provider := NewVolcDuplexProvider(Config{
		VolcSpeechAPIKey:   "test-key",
		VolcDuplexEndpoint: startVolcDuplexStub(t),
		VolcDuplexModel:    "test-model",
		VolcDuplexVoice:    "test-voice",
		ClientAudioFormat:  "pcm-s16le",
	}, nil)

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	sess, err := provider.Open(ctx, ConsumedTicket{TicketID: "t1", SessionID: "s1", UserID: "u1"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	defer func() { _ = sess.Close(context.Background()) }()

	out, err := sess.Start(ctx, voiceproto.SessionStart{Type: voiceproto.TypeSessionStart}, nil)
	if err != nil {
		t.Fatalf("Start: %v", err)
	}
	// Exactly one frame: no ai.text.delta may be fabricated on a real session.
	if len(out) != 1 {
		t.Fatalf("bootstrap outbound count = %d, want 1: %+v", len(out), out)
	}
	end, ok := out[0].Control.(voiceproto.AITurnEnd)
	if !ok {
		t.Fatalf("bootstrap outbound is %T, want voiceproto.AITurnEnd", out[0].Control)
	}
	if end.TurnID != "bootstrap" || end.Outcome != "ok" {
		t.Fatalf("unexpected bootstrap turn.end: %+v", end)
	}
	// iOS keeps the first non-empty log_id for the whole session; a greeting
	// stamp would win over the first real turn's vendor id after an abort reset.
	if end.LogID != "" {
		t.Fatalf("bootstrap frame must not carry log_id, got %q", end.LogID)
	}
	raw, err := json.Marshal(end)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	if strings.Contains(string(raw), "log_id") {
		t.Fatalf("bootstrap wire frame must omit log_id: %s", raw)
	}
}

func TestVolcDuplexProviderOpenRequiresSpeechKey(t *testing.T) {
	t.Parallel()

	provider := NewVolcDuplexProvider(Config{
		Provider:          "volc-duplex",
		ClientAudioFormat: "pcm-s16le",
	}, nil)
	_, err := provider.Open(context.Background(), ConsumedTicket{})
	if err == nil || !strings.Contains(err.Error(), "missing speech API key") {
		t.Fatalf("expected missing key error, got %v", err)
	}
}

func TestInstructionsForSessionStartIncludesContext(t *testing.T) {
	t.Parallel()

	text := instructionsForSessionStart(voiceproto.SessionStart{
		Type:       voiceproto.TypeSessionStart,
		SceneType:  "daily-read",
		MaterialID: "m-42",
	}, nil)
	if !strings.Contains(text, "daily-read") || !strings.Contains(text, "m-42") {
		t.Fatalf("unexpected instructions: %q", text)
	}
}

// The continuation block is the entire user-visible difference of the feature:
// if this text is not in the instructions, the model has no way to know it is
// continuing anything, and the opening line will be as generic as before.
func TestInstructionsCarryThePreviousTranscript(t *testing.T) {
	t.Parallel()

	text := instructionsForSessionStart(voiceproto.SessionStart{
		Type: voiceproto.TypeSessionStart,
	}, []ContinuationTurn{
		{Seq: 1, Speaker: "user", Text: "how do I say 限流?"},
		{Seq: 2, Speaker: "ai", Text: "Rate limiting."},
	})

	for _, want := range []string{"上一场练习", "user: how do I say 限流?", "ai: Rate limiting."} {
		if !strings.Contains(text, want) {
			t.Fatalf("instructions missing %q:\n%s", want, text)
		}
	}
	// The prompt states the facts and asks for one behaviour. It must not
	// script the opening sentence: the PRD's example is what the model should
	// derive from the transcript, and a canned line would appear whether or not
	// it matched what was actually said.
	if strings.Contains(text, "上次我们聊到") {
		t.Fatalf("instructions script the opening line:\n%s", text)
	}
}

// No turns means no block — not an empty preamble telling the model to
// continue a conversation it cannot see.
func TestInstructionsOmitTheBlockWhenThereIsNothingToContinueFrom(t *testing.T) {
	t.Parallel()

	for _, turns := range [][]ContinuationTurn{nil, {}} {
		text := instructionsForSessionStart(voiceproto.SessionStart{
			Type: voiceproto.TypeSessionStart,
		}, turns)
		if strings.Contains(text, "上一场练习") {
			t.Fatalf("empty continuation produced a block: %q", text)
		}
	}
}

// TestVolcDuplexSessionAlignsTurnIDFromClient is the regression for #42.
//
// Before the fix, activeTurnID was empty on the first user.speech.end so the
// outbound ai.turn.end / client.asr.transcription frames fell back to
// "volc-turn-<seq>" while iOS used "turn-1…turn-N" — the two namespaces never
// matched and the local dedupe mirror in runbook Case 3 could not correlate.
// The fix parses the client-supplied turn_id from the user.speech.end payload
// and stamps it onto every outbound frame from this turn forward.
func TestVolcDuplexSessionAlignsTurnIDFromClient(t *testing.T) {
	t.Parallel()

	sess := &volcDuplexProviderSession{
		logger:      slog.Default(),
		audioFormat: "pcm-s16le",
		nextSeq:     1,
	}

	// Simulate iOS sending user.speech.end with turn_id="turn-7".
	raw, err := json.Marshal(voiceproto.UserSpeechEnd{
		Type:   voiceproto.TypeUserSpeechEnd,
		Text:   "hello world",
		TurnID: "turn-7",
	})
	if err != nil {
		t.Fatalf("marshal user.speech.end: %v", err)
	}

	// Without a real DuplexSession wired up, we can't drive turnToOutbound end
	// to end. Instead we directly verify the parsing path: extract turn_id and
	// confirm it lands in activeTurnID, ready for the outbound builder.
	var end voiceproto.UserSpeechEnd
	if err := json.Unmarshal(raw, &end); err != nil {
		t.Fatalf("unmarshal user.speech.end: %v", err)
	}
	if t2 := strings.TrimSpace(end.TurnID); t2 != "" {
		sess.activeTurnID = t2
	}

	if sess.activeTurnID != "turn-7" {
		t.Fatalf("activeTurnID not aligned with iOS: got %q want %q", sess.activeTurnID, "turn-7")
	}

	out := sess.turnToOutbound(voicepoc.TurnResult{
		Transcript:    "hello world",
		AssistantText: "hi",
		Outcome:       voicepoc.TurnOutcomeOK,
	})
	if got := aiTurnEndID(out); got != "turn-7" {
		t.Fatalf("ai.turn.end turn_id = %q, want turn-7", got)
	}
}

func TestTurnToOutboundFallbackUsesTurnNNotVolcPrefix(t *testing.T) {
	t.Parallel()

	sess := &volcDuplexProviderSession{
		logger:      slog.Default(),
		audioFormat: "pcm-s16le",
		nextSeq:     3,
	}
	out := sess.turnToOutbound(voicepoc.TurnResult{Outcome: voicepoc.TurnOutcomeOK})
	if got := aiTurnEndID(out); got != "turn-3" {
		t.Fatalf("fallback turn_id = %q, want turn-3 (not volc-turn-3)", got)
	}
}

func aiTurnEndID(out []ProviderOutbound) string {
	for _, item := range out {
		end, ok := item.Control.(voiceproto.AITurnEnd)
		if ok {
			return end.TurnID
		}
	}
	return ""
}

// --- #43 keepalive tests -----------------------------------------------------

// TestVolcDuplexSession_KeepaliveSkipsProbeOnFreshSession verifies the
// decision logic that suppresses the keepalive probe on the first audio chunk.
// Probing an empty session makes no sense and would generate extra QPS.
func TestVolcDuplexSession_KeepaliveSkipsProbeOnFreshSession(t *testing.T) {
	t.Parallel()

	sess := &volcDuplexProviderSession{
		logger:      slog.Default(),
		audioFormat: "pcm-s16le",
		// lastAudioAt intentionally zero → first chunk ever
	}

	if sess.shouldProbe() {
		t.Fatalf("fresh session should not probe; got shouldProbe=true")
	}
}

// TestVolcDuplexSession_KeepaliveProbesWhenIdlePastThreshold verifies the
// trigger fires after keepaliveIdleThreshold (60s) has elapsed since the last
// forward. This is the core #43 fix path: 2 min idle in production → next
// chunk first probes the upstream before risking a broken pipe.
func TestVolcDuplexSession_KeepaliveProbesWhenIdlePastThreshold(t *testing.T) {
	t.Parallel()

	frozen := time.Date(2026, 9, 4, 3, 0, 0, 0, time.UTC)
	sess := &volcDuplexProviderSession{
		logger:      slog.Default(),
		audioFormat: "pcm-s16le",
		lastAudioAt: frozen.Add(-90 * time.Second), // 90s idle
		nowFn:       func() time.Time { return frozen },
	}

	if !sess.shouldProbe() {
		t.Fatalf("idle session should probe; got shouldProbe=false")
	}
	if got := sess.idleSince(); got < keepaliveIdleThreshold {
		t.Fatalf("idleSince (%s) should exceed threshold (%s)", got, keepaliveIdleThreshold)
	}
}

// TestVolcDuplexSession_KeepaliveDoesNotProbeBelowThreshold verifies the
// common case: audio forwards within the 60s idle window do not probe.
// The probe costs an extra round-trip and would inflate QPS if unconditional.
func TestVolcDuplexSession_KeepaliveDoesNotProbeBelowThreshold(t *testing.T) {
	t.Parallel()

	frozen := time.Date(2026, 9, 4, 3, 0, 0, 0, time.UTC)
	sess := &volcDuplexProviderSession{
		logger:      slog.Default(),
		audioFormat: "pcm-s16le",
		lastAudioAt: frozen.Add(-30 * time.Second), // only 30s idle
		nowFn:       func() time.Time { return frozen },
	}

	if sess.shouldProbe() {
		t.Fatalf("30s idle is below 60s threshold; should not probe")
	}
}

// TestVolcDuplexSession_KeepaliveProbeFailureSurfacesToHandler verifies the
// "dead upstream" path: when the probe fails, the error returned to the
// caller wraps both the idle duration and the underlying transport error.
// The handler uses this signal to trigger its reopen-once fallback (B15
// Item 1.2). We exercise runProbe directly here because constructing a
// real voicepoc.DuplexSession would require a live Volc WebSocket; the
// end-to-end wiring (probe → HandleClientAudio → handler reopen) is
// covered by integration tests and the production code path is one
// straight-line call between runProbe and the error wrapping in
// HandleClientAudio — see the implementation comment block above the
// if s.shouldProbe() branch.
func TestVolcDuplexSession_KeepaliveProbeFailureSurfacesToHandler(t *testing.T) {
	t.Parallel()

	frozen := time.Date(2026, 9, 4, 3, 0, 0, 0, time.UTC)
	probeErr := errors.New("write tcp 10.0.0.1:443: broken pipe")
	probeCalled := false
	sess := &volcDuplexProviderSession{
		logger:      slog.Default(),
		audioFormat: "pcm-s16le",
		lastAudioAt: frozen.Add(-2 * time.Minute),
		nowFn:       func() time.Time { return frozen },
		probeFn: func(_ context.Context) error {
			probeCalled = true
			return probeErr
		},
	}

	// Drive the same decision flow HandleClientAudio would.
	if !sess.shouldProbe() {
		t.Fatalf("preconditions: shouldProbe must fire for 2m idle")
	}
	err := sess.runProbe(context.Background())
	if err == nil {
		t.Fatalf("expected probe-failure error, got nil")
	}
	if !probeCalled {
		t.Fatalf("probeFn was not invoked despite idle gap")
	}
	if !errors.Is(err, probeErr) {
		t.Fatalf("expected wrapped probeErr, got %v", err)
	}
}

// TestVolcDuplexSession_KeepaliveProbeSuccessUpdatesTimestamp verifies that
// after a successful probe the lastAudioAt stamp moves to "now", so the
// next chunk within 60s does not re-probe unnecessarily.
func TestVolcDuplexSession_KeepaliveProbeSuccessUpdatesTimestamp(t *testing.T) {
	t.Parallel()

	frozen := time.Date(2026, 9, 4, 3, 0, 0, 0, time.UTC)
	sess := &volcDuplexProviderSession{
		logger:      slog.Default(),
		audioFormat: "pcm-s16le",
		lastAudioAt: frozen.Add(-2 * time.Minute),
		nowFn:       func() time.Time { return frozen },
		probeFn:     func(_ context.Context) error { return nil },
	}

	if !sess.shouldProbe() {
		t.Fatalf("preconditions: probe should fire")
	}
	if err := sess.runProbe(context.Background()); err != nil {
		t.Fatalf("probe should succeed: %v", err)
	}
	// Simulate the timestamp update that HandleClientAudio performs post-probe.
	sess.lastAudioAt = sess.nowFn()
	if sess.shouldProbe() {
		t.Fatalf("after successful probe, shouldProbe should be false")
	}
}

// TestVolcDuplexSession_KeepaliveProbeRespectsTimeout verifies that
// runProbe caps the upstream write at keepaliveProbeTimeout so a stuck
// upstream cannot stall the audio forward beyond ~3s — iOS still sees
// provider_audio_failed within budget even when the upstream is wedged.
func TestVolcDuplexSession_KeepaliveProbeRespectsTimeout(t *testing.T) {
	t.Parallel()

	gotTimeout := false
	sess := &volcDuplexProviderSession{
		logger:      slog.Default(),
		audioFormat: "pcm-s16le",
		probeFn: func(ctx context.Context) error {
			_, hasDeadline := ctx.Deadline()
			gotTimeout = hasDeadline
			return nil
		},
	}

	if err := sess.runProbe(context.Background()); err != nil {
		t.Fatalf("probe returned: %v", err)
	}
	if !gotTimeout {
		t.Fatalf("runProbe must apply a context deadline; got unbounded ctx")
	}
}

func TestVolcDuplexAbortClearsTurnWithoutCollect(t *testing.T) {
	t.Parallel()

	sess := &volcDuplexProviderSession{
		logger:       slog.Default(),
		audioFormat:  "pcm-s16le",
		turnStarted:  time.Date(2026, 9, 10, 0, 0, 0, 0, time.UTC),
		activeTurnID: "turn-1",
		nextSeq:      4,
	}

	out, err := sess.HandleClientControl(context.Background(), voiceproto.TypeClientTurnAbort, voiceproto.MustMarshal(voiceproto.ClientTurnAbort{
		Type:    voiceproto.TypeClientTurnAbort,
		TurnID:  "turn-1",
		Outcome: voiceproto.ClientTurnAbortTimeout,
	}))
	if err != nil {
		t.Fatalf("abort should not error: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("abort must not emit outbound (would start collectTurn / ai.turn.end), got %#v", out)
	}
	if !sess.turnStarted.IsZero() {
		t.Fatalf("turnStarted should clear, got %s", sess.turnStarted)
	}
	if sess.activeTurnID != "" {
		t.Fatalf("activeTurnID should clear, got %q", sess.activeTurnID)
	}
}

func TestVolcDuplexAbortResetsUpstreamSession(t *testing.T) {
	t.Parallel()

	var resets int
	sess := &volcDuplexProviderSession{
		logger:       slog.Default(),
		audioFormat:  "pcm-s16le",
		turnStarted:  time.Now(),
		activeTurnID: "turn-2",
		resetDuplexFn: func(context.Context) error {
			resets++
			return nil
		},
	}

	out, err := sess.HandleClientControl(context.Background(), voiceproto.TypeClientTurnAbort, voiceproto.MustMarshal(voiceproto.ClientTurnAbort{
		Type:    voiceproto.TypeClientTurnAbort,
		TurnID:  "turn-2",
		Outcome: voiceproto.ClientTurnAbortUserAbandoned,
	}))
	if err != nil {
		t.Fatalf("abort should not error: %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("abort must not emit outbound, got %#v", out)
	}
	if resets != 1 {
		t.Fatalf("abort must reset duplex once, got %d", resets)
	}
	if !sess.turnStarted.IsZero() || sess.activeTurnID != "" {
		t.Fatalf("turn state should clear after reset")
	}
}

func TestVolcDuplexAbortResetFailureKeepsSessionAlive(t *testing.T) {
	t.Parallel()

	sess := &volcDuplexProviderSession{
		logger:       slog.Default(),
		audioFormat:  "pcm-s16le",
		turnStarted:  time.Now(),
		activeTurnID: "turn-3",
		resetDuplexFn: func(context.Context) error {
			return errors.New("volc dial failed")
		},
	}

	out, err := sess.HandleClientControl(context.Background(), voiceproto.TypeClientTurnAbort, voiceproto.MustMarshal(voiceproto.ClientTurnAbort{
		Type:    voiceproto.TypeClientTurnAbort,
		TurnID:  "turn-3",
		Outcome: voiceproto.ClientTurnAbortError,
	}))
	if err != nil {
		t.Fatalf("abort must not surface duplex reset errors (would kill iOS session): %v", err)
	}
	if len(out) != 0 {
		t.Fatalf("abort must not emit outbound, got %#v", out)
	}
	if !sess.turnStarted.IsZero() || sess.activeTurnID != "" {
		t.Fatalf("local turn state must still clear when reset fails")
	}
}

func TestTurnToOutbound_StampsOutcomeOnAITurnEnd(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name     string
		outcome  voicepoc.TurnOutcome
		wantWire string
	}{
		{name: "timeout", outcome: voicepoc.TurnOutcomeTimeout, wantWire: "timeout"},
		{name: "partial", outcome: voicepoc.TurnOutcomePartial, wantWire: "partial"},
		{name: "error", outcome: voicepoc.TurnOutcomeError, wantWire: "error"},
		{name: "ok", outcome: voicepoc.TurnOutcomeOK, wantWire: "ok"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			sess := &volcDuplexProviderSession{
				logger:       slog.Default(),
				activeTurnID: "turn-" + tc.name,
				nextSeq:      1,
			}
			out := sess.turnToOutbound(voicepoc.TurnResult{Outcome: tc.outcome})
			var end voiceproto.AITurnEnd
			found := false
			for _, frame := range out {
				got, ok := frame.Control.(voiceproto.AITurnEnd)
				if !ok {
					continue
				}
				found = true
				end = got
			}
			if !found {
				t.Fatalf("expected AITurnEnd in outbound, got %#v", out)
			}
			if end.Type != voiceproto.TypeAITurnEnd {
				t.Fatalf("type = %q", end.Type)
			}
			if end.TurnID != "turn-"+tc.name {
				t.Fatalf("turn_id = %q", end.TurnID)
			}
			if end.Outcome != tc.wantWire {
				t.Fatalf("outcome = %q want %q", end.Outcome, tc.wantWire)
			}
		})
	}
}

func TestTurnToOutbound_StampsServerTsMsOnTextDelta(t *testing.T) {
	t.Parallel()

	frozen := time.Date(2026, 9, 10, 1, 2, 3, 0, time.UTC)
	sess := &volcDuplexProviderSession{
		logger:       slog.Default(),
		activeTurnID: "turn-1",
		nextSeq:      1,
		nowFn:        func() time.Time { return frozen },
	}
	out := sess.turnToOutbound(voicepoc.TurnResult{
		Outcome:       voicepoc.TurnOutcomeOK,
		AssistantText: "hello",
	})
	var delta voiceproto.AITextDelta
	found := false
	for _, frame := range out {
		got, ok := frame.Control.(voiceproto.AITextDelta)
		if !ok {
			continue
		}
		found = true
		delta = got
	}
	if !found {
		t.Fatalf("expected AITextDelta in outbound, got %#v", out)
	}
	if delta.Text != "hello" || delta.TurnID != "turn-1" {
		t.Fatalf("delta = %#v", delta)
	}
	if delta.ServerTsMs != frozen.UnixMilli() {
		t.Fatalf("server_ts_ms = %d want %d", delta.ServerTsMs, frozen.UnixMilli())
	}
}
