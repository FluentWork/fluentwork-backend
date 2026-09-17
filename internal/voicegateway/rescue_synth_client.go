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
}

// DefaultRescueSynthTimeout bounds one synthesis, and it is sized against the
// rung's budget rather than picked round.
//
// Measured on 2026-09-18 with the configured ladder voice: 565 ms to first audio,
// 951 ms to the last byte, for a 12-word English rung. The orchestrator wraps the
// whole rung — text generation *and* synthesis — in one DefaultRescueLevel1After
// (3s) deadline, and text generation alone measures ~2s. One second for audio
// therefore fits inside what is left with a little room to spare; exceeding it
// costs the rung its voice and never its text, which is already on the wire by
// the time this call is made.
const DefaultRescueSynthTimeout = 1000 * time.Millisecond

// NewHTTPRescueSynthesizer constructs the client.
func NewHTTPRescueSynthesizer(baseURL, token, voiceID string, logger *slog.Logger) *HTTPRescueSynthesizer {
	if logger == nil {
		logger = slog.Default()
	}
	return &HTTPRescueSynthesizer{
		BaseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		Token:   strings.TrimSpace(token),
		VoiceID: strings.TrimSpace(voiceID),
		Client:  &http.Client{},
		Logger:  logger.With("component", "voicegateway.rescue_synth"),
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
		var cancel context.CancelFunc
		ctx, cancel = context.WithTimeout(ctx, DefaultRescueSynthTimeout)
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
