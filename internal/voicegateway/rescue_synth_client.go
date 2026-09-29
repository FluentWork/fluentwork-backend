package voicegateway

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"
)

// HTTPRescueSynthesizer speaks a rescue rung through app-server's TTS.
//
// Same reasoning as the ladder's text (see HTTPRescueGenerator): synthesis lives
// with every other model and vendor call, in app-server, where the cost ledger
// and the voice catalog are. The gateway sends text and receives PCM.
//
// The returned PCM is whatever the client plays: 16 kHz mono s16le (docs/92 §3).
type HTTPRescueSynthesizer struct {
	BaseURL string
	Token   string
	// VoiceID is the speaker and pace. Empty uses app-server's default voice,
	// which is *not* the rescue voice — the ladder is deliberately slower than
	// the conversation, and that is a property of the request, not of the
	// gateway's opinion.
	VoiceID string
	Client  *http.Client
	Logger  *slog.Logger
	// timeout is the per-request budget, applied in Synthesize only when the
	// caller's context carries no deadline of its own. Zero means
	// DefaultRescueSynthTimeout.
	timeout time.Duration
}

// DefaultRescueSynthTimeout bounds one synthesis.
//
// Measured on 2026-09-18 with the configured ladder voice: 565 ms to first audio,
// 951 ms to the last byte, for a 12-word English rung. The first value here was
// 1000 ms — a 5% margin over that one measurement, which is a coincidence rather
// than a budget, and 2026-09-30 真机 collected on it: the middle rung's synthesis
// came back `context deadline exceeded` and that rung lost its voice (and, since
// the client does not render ladder text, the rung itself).
//
// Two facts size this number, and both changed with that fix:
//
//   - the frame no longer waits for the audio (see
//     RescueOrchestrator.GenerateAndSynthesize), so a generous budget costs the
//     learner nothing in prompt latency — only "the voice lands later";
//   - what actually caps it is `Config.validateRescueTiming`: the synth budget
//     must stay **under** `VOICE_RESCUE_LEVEL1_AFTER`, because audio that
//     outlives the rung spacing lands after the next rung is due. A shortened
//     ladder (the test's 2s rungs) is legitimate, so the default has to clear
//     the *shortest* spacing the config treats as sane. 1500 ms does, at ~1.6×
//     the measured worst case — the number is set by that invariant, not by
//     taste. (An earlier revision of this fix used 2000 ms and broke
//     `TestValidateRescueTimingsAllowShorteningRungsAlone`.)
//
// It is **not** wrapped in the rung's deadline. Generation has its own budget
// (RescueOrchestrator.generateText) and this call has its own; an earlier
// revision of this comment claimed the orchestrator wrapped both in one 3s rung
// deadline, which was never true and is what produced the 5% margin above.
//
// Configurable through VOICE_RESCUE_SYNTH_TIMEOUT.
const DefaultRescueSynthTimeout = 1500 * time.Millisecond

// NewHTTPRescueSynthesizer constructs the client. A timeout of zero or less uses
// DefaultRescueSynthTimeout.
func NewHTTPRescueSynthesizer(baseURL, token, voiceID string, timeout time.Duration, logger *slog.Logger) *HTTPRescueSynthesizer {
	if logger == nil {
		logger = slog.Default()
	}
	if timeout <= 0 {
		timeout = DefaultRescueSynthTimeout
	}
	return &HTTPRescueSynthesizer{
		BaseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		Token:   strings.TrimSpace(token),
		VoiceID: strings.TrimSpace(voiceID),
		Client:  &http.Client{},
		Logger:  logger.With("component", "voicegateway.rescue_synth"),
		timeout: timeout,
	}
}

type rescueSynthBody struct {
	Text    string `json:"text"`
	VoiceID string `json:"voice_id,omitempty"`
}

// rescueSynthEnvelope is app-server's success shape.
//
// Decoded as a local type rather than imported from the tts package: the gateway
// must not take a dependency on app-server's packages to talk to it over HTTP.
// The cost of that independence is this second declaration of the shape, which is
// why rescue_synth_client_test.go drives the real handler instead of a fixture —
// a fixture would restate the same assumption and could not catch it drifting.
type rescueSynthEnvelope struct {
	VoiceID     string `json:"voice_id"`
	Chunks      int    `json:"chunks"`
	AudioBase64 string `json:"audio_base64"`
}

// Synthesize implements RescueSynthesizer.
func (s *HTTPRescueSynthesizer) Synthesize(ctx context.Context, text, _ string) (RescueAudio, error) {
	if s == nil || s.BaseURL == "" {
		return RescueAudio{}, fmt.Errorf("rescue synthesizer is not configured")
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return RescueAudio{}, fmt.Errorf("rescue synthesizer: text is empty")
	}
	raw, err := json.Marshal(rescueSynthBody{Text: text, VoiceID: s.VoiceID})
	if err != nil {
		return RescueAudio{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, s.BaseURL+"/internal/v1/tts/synthesize", bytes.NewReader(raw))
	if err != nil {
		return RescueAudio{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Token", s.Token)

	client := s.Client
	if client == nil {
		client = &http.Client{}
	}
	if _, ok := ctx.Deadline(); !ok {
		budget := s.timeout
		if budget <= 0 {
			budget = DefaultRescueSynthTimeout
		}
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, budget)
		defer cancel()
		req = req.WithContext(ctx)
	}
	res, err := client.Do(req)
	if err != nil {
		return RescueAudio{}, err
	}
	defer func() { _ = res.Body.Close() }()
	payload, err := io.ReadAll(io.LimitReader(res.Body, 8<<20))
	if err != nil {
		return RescueAudio{}, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return RescueAudio{}, fmt.Errorf("rescue synth http=%d body=%s", res.StatusCode, strings.TrimSpace(string(payload)))
	}
	var envelope rescueSynthEnvelope
	if err := json.Unmarshal(payload, &envelope); err != nil {
		return RescueAudio{}, fmt.Errorf("decode rescue synth: %w", err)
	}
	pcm, err := decodeBase64Audio(envelope.AudioBase64)
	if err != nil {
		return RescueAudio{}, fmt.Errorf("decode rescue synth audio: %w", err)
	}
	if len(pcm) == 0 {
		return RescueAudio{}, fmt.Errorf("rescue synth came back empty")
	}
	return RescueAudio{
		PCM:        pcm,
		SampleRate: RescueAudioSampleRate,
		Codec:      "pcm",
		VoiceID:    envelope.VoiceID,
	}, nil
}

// RescueAudioSampleRate is the rate the client plays at. The player builds every
// buffer as 16 kHz mono int16, so bytes at another rate come out at the wrong
// speed and pitch — this is a property of the client, not a preference.
const RescueAudioSampleRate = 16000

// decodeBase64Audio accepts the standard and unpadded encodings, because the
// difference is a transport detail and refusing one of them turns a decodable
// rung into a silent one.
func decodeBase64Audio(data string) ([]byte, error) {
	decoded, err := base64.StdEncoding.DecodeString(data)
	if err == nil {
		return decoded, nil
	}
	decoded, rawErr := base64.RawStdEncoding.DecodeString(data)
	if rawErr != nil {
		return nil, err
	}
	return decoded, nil
}
