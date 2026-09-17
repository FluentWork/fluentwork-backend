package tts

import (
	"bytes"
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"math"
	"net/http"
	"strings"
	"sync/atomic"
	"time"

	"github.com/google/uuid"
)

const (
	defaultVolcTTSEndpoint   = "https://openspeech.bytedance.com/api/v3/tts/unidirectional"
	defaultVolcTTSResourceID = "seed-tts-2.0"
	// defaultVolcTTSSpeaker is a **2.0** speaker, and that is not a preference:
	// the account's resource is seed-tts-2.0, which only accepts speakers of the
	// 2.0 lineage (`*_uranus_bigtts`). The previous value was a 1.0 speaker
	// (`*_jupiter_bigtts`) and every synthesis came back
	// `55000000 resource ID is mismatched with speaker related resource` — a
	// mismatch reported as if it were a missing entitlement, which is why this
	// sat for a week looking like a vendor authorization problem (docs/92 §2).
	defaultVolcTTSSpeaker = "zh_female_vv_uranus_bigtts"
	// defaultVolcTTSFormat/SampleRate are what the voice gateway's client plays:
	// 16 kHz mono s16le PCM. The client's RawPCM16FrameDecoder hands the payload
	// straight to the audio engine, which builds every buffer as 16 kHz mono
	// int16 — any other rate comes out at the wrong speed and pitch. Opus would
	// halve the bytes, but the client's Opus decoder is a deliberate stub, so PCM
	// is the format that needs no new code on either side (docs/92 §3).
	defaultVolcTTSFormat     = "pcm"
	defaultVolcTTSSampleRate = 16000
	defaultVolcTTSUID        = "fluentwork"
	volcTTSSuccessCode       = 20000000
	volcTTSMaxAttempts       = 2
)

var (
	// ErrMissingAPIKey means the streaming provider has no X-Api-Key.
	ErrMissingAPIKey = errors.New("tts: missing API key")
	// ErrHTTPStatus means the TTS endpoint returned a non-success HTTP status.
	ErrHTTPStatus = errors.New("tts: unexpected http status")
)

// ErrSpeakerResourceMismatch means the configured speaker does not belong to the
// resource generation the account is entitled to. It is caught here rather than
// left to the vendor because the vendor reports it as code 55000000, which reads
// like a missing entitlement and sends you to the console to argue about SKUs
// when the fix is a different speaker name (docs/92 §2).
var ErrSpeakerResourceMismatch = errors.New("tts: speaker does not belong to the configured resource")

// speakerMatchesResource reports whether a speaker may be used with a resource id.
//
// The rule is the vendor's: the 2.0 resource serves 2.0 speakers, whose ids end
// in `_uranus_bigtts`. Unknown resource ids are not judged — an account with a
// different SKU knows its own pairing, and refusing to synthesize on a guess
// would be worse than the vendor's own error.
func speakerMatchesResource(resourceID, speaker string) bool {
	resourceID = strings.TrimSpace(resourceID)
	speaker = strings.TrimSpace(speaker)
	if resourceID != "seed-tts-2.0" {
		return true
	}
	return strings.HasSuffix(speaker, "_uranus_bigtts")
}

// VolcStreamingProvider calls Volc HTTP Chunked unidirectional TTS (no SDK).
//
// X-Api-Resource-Id is the product SKU (default seed-tts-2.0). VoiceConfig.VoiceID
// is the speaker id in req_params.speaker. Audio bytes are base64-decoded from
// concatenated JSON objects on the chunked response body.
type VolcStreamingProvider struct {
	APIKey     string
	Endpoint   string
	ResourceID string
	Format     string
	SampleRate int
	HTTPClient *http.Client
	Logger     *slog.Logger

	now          func() time.Time
	newRequestID func() string
	closed       atomic.Bool
}

var _ Provider = (*VolcStreamingProvider)(nil)

var defaultVolcHTTPClient = &http.Client{
	Transport: &http.Transport{
		Proxy:                 http.ProxyFromEnvironment,
		ForceAttemptHTTP2:     true,
		MaxIdleConns:          16,
		IdleConnTimeout:       time.Minute,
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 5 * time.Second,
		ExpectContinueTimeout: 1 * time.Second,
	},
}

type volcTTSRequest struct {
	User      volcTTSUser      `json:"user"`
	Namespace string           `json:"namespace,omitempty"`
	ReqParams volcTTSReqParams `json:"req_params"`
}

type volcTTSUser struct {
	UID string `json:"uid"`
}

type volcTTSReqParams struct {
	Text        string          `json:"text"`
	Speaker     string          `json:"speaker"`
	AudioParams volcAudioParams `json:"audio_params"`
}

type volcAudioParams struct {
	Format     string `json:"format"`
	SampleRate int    `json:"sample_rate"`
	SpeechRate int    `json:"speech_rate"`
}

type volcTTSFrame struct {
	Code    int    `json:"code"`
	Message string `json:"message"`
	Data    string `json:"data"`
}

// Ping reports whether the provider can accept a new Stream call.
func (p *VolcStreamingProvider) Ping(context.Context) error {
	if p == nil || p.closed.Load() {
		return ErrClosed
	}
	if strings.TrimSpace(p.APIKey) == "" {
		return ErrMissingAPIKey
	}
	return nil
}

// Close prevents further Stream and Ping calls. It is idempotent.
func (p *VolcStreamingProvider) Close() error {
	if p != nil {
		p.closed.Store(true)
	}
	return nil
}

// Stream POSTs text to Volc unidirectional TTS and yields decoded audio chunks.
// HTTP 5xx is retried once before the channel is returned. The channel is closed
// when the server finishes, disconnects, or ctx is cancelled.
func (p *VolcStreamingProvider) Stream(ctx context.Context, text string, voice VoiceConfig) (<-chan AudioChunk, error) {
	if p == nil || p.closed.Load() {
		return nil, ErrClosed
	}
	if strings.TrimSpace(p.APIKey) == "" {
		return nil, ErrMissingAPIKey
	}
	text, voice, err := NormalizeStreamInput(text, voice)
	if err != nil {
		return nil, err
	}
	// Refuse the pairing the vendor would refuse, with a message that names the
	// actual fix. See ErrSpeakerResourceMismatch.
	if !speakerMatchesResource(p.resourceID(), voice.VoiceID) {
		return nil, fmt.Errorf("%w: speaker %q with resource %q",
			ErrSpeakerResourceMismatch, voice.VoiceID, p.resourceID())
	}

	raw, err := json.Marshal(p.buildRequest(text, voice))
	if err != nil {
		return nil, err
	}
	requestID := p.requestID()

	var resp *http.Response
	for attempt := 1; attempt <= volcTTSMaxAttempts; attempt++ {
		resp, err = p.post(ctx, raw, requestID)
		if err != nil {
			return nil, err
		}
		if resp.StatusCode >= 500 && resp.StatusCode <= 599 && attempt < volcTTSMaxAttempts {
			p.log("volc tts 5xx retry", "status", resp.StatusCode, "request_id", requestID, "log_id", resp.Header.Get("X-Tt-Logid"))
			_, _ = io.Copy(io.Discard, resp.Body)
			_ = resp.Body.Close()
			continue
		}
		if resp.StatusCode != http.StatusOK {
			logID := resp.Header.Get("X-Tt-Logid")
			_ = resp.Body.Close()
			return nil, &httpStatusError{Status: resp.StatusCode, LogID: logID}
		}
		break
	}

	out := make(chan AudioChunk)
	go p.readStream(ctx, resp, out)
	return out, nil
}

func (p *VolcStreamingProvider) readStream(ctx context.Context, resp *http.Response, out chan<- AudioChunk) {
	defer close(out)
	defer func() { _ = resp.Body.Close() }()

	detectedAt := p.nowMilli()
	dec := json.NewDecoder(resp.Body)
	seq := 0
	var pending *AudioChunk

	emitPending := func(final bool) bool {
		if pending == nil {
			return true
		}
		chunk := *pending
		chunk.IsFinal = final
		select {
		case <-ctx.Done():
			return false
		case out <- chunk:
			pending = nil
			return true
		}
	}

	for {
		if ctx.Err() != nil {
			return
		}
		var frame volcTTSFrame
		if err := dec.Decode(&frame); err != nil {
			if errors.Is(err, io.EOF) {
				_ = emitPending(true)
			}
			return
		}
		switch {
		case frame.Code == volcTTSSuccessCode:
			_ = emitPending(true)
			return
		case frame.Code != 0:
			p.warn("volc tts protocol error",
				"code", frame.Code,
				"message", frame.Message,
				"log_id", resp.Header.Get("X-Tt-Logid"),
			)
			return
		case strings.TrimSpace(frame.Data) == "":
			continue
		}

		data, err := decodeVolcAudio(frame.Data)
		if err != nil {
			p.warn("volc tts audio decode", "error", err)
			return
		}
		if !emitPending(false) {
			return
		}
		pending = &AudioChunk{
			Data:       data,
			Seq:        seq,
			DetectedAt: detectedAt,
		}
		seq++
	}
}

func (p *VolcStreamingProvider) post(ctx context.Context, raw []byte, requestID string) (*http.Response, error) {
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, p.endpoint(), bytes.NewReader(raw))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Api-Key", strings.TrimSpace(p.APIKey))
	req.Header.Set("X-Api-Resource-Id", p.resourceID())
	req.Header.Set("X-Api-Request-Id", requestID)
	return p.client().Do(req)
}

func (p *VolcStreamingProvider) buildRequest(text string, voice VoiceConfig) volcTTSRequest {
	speaker := strings.TrimSpace(voice.VoiceID)
	if speaker == "" {
		speaker = defaultVolcTTSSpeaker
	}
	format := strings.TrimSpace(p.Format)
	if format == "" {
		format = defaultVolcTTSFormat
	}
	sampleRate := p.SampleRate
	if sampleRate <= 0 {
		sampleRate = defaultVolcTTSSampleRate
	}
	return volcTTSRequest{
		User:      volcTTSUser{UID: defaultVolcTTSUID},
		Namespace: "BidirectionalTTS",
		ReqParams: volcTTSReqParams{
			Text:    text,
			Speaker: speaker,
			AudioParams: volcAudioParams{
				Format:     format,
				SampleRate: sampleRate,
				SpeechRate: speedToSpeechRate(voice.Speed),
			},
		},
	}
}

func (p *VolcStreamingProvider) client() *http.Client {
	if p != nil && p.HTTPClient != nil {
		return p.HTTPClient
	}
	return defaultVolcHTTPClient
}

func (p *VolcStreamingProvider) endpoint() string {
	if p != nil && strings.TrimSpace(p.Endpoint) != "" {
		return strings.TrimSpace(p.Endpoint)
	}
	return defaultVolcTTSEndpoint
}

func (p *VolcStreamingProvider) resourceID() string {
	if p != nil && strings.TrimSpace(p.ResourceID) != "" {
		return strings.TrimSpace(p.ResourceID)
	}
	return defaultVolcTTSResourceID
}

func (p *VolcStreamingProvider) requestID() string {
	if p != nil && p.newRequestID != nil {
		return p.newRequestID()
	}
	return uuid.NewString()
}

func (p *VolcStreamingProvider) nowMilli() int64 {
	now := time.Now
	if p != nil && p.now != nil {
		now = p.now
	}
	return now().UnixMilli()
}

func (p *VolcStreamingProvider) log(msg string, args ...any) {
	if p == nil || p.Logger == nil {
		return
	}
	p.Logger.Info(msg, args...)
}

func (p *VolcStreamingProvider) warn(msg string, args ...any) {
	if p == nil || p.Logger == nil {
		return
	}
	p.Logger.Warn(msg, args...)
}

func speedToSpeechRate(speed float64) int {
	rate := int(math.Round((speed - 1) * 100))
	if rate < -50 {
		return -50
	}
	if rate > 100 {
		return 100
	}
	return rate
}

func decodeVolcAudio(data string) ([]byte, error) {
	decoded, err := base64.StdEncoding.DecodeString(data)
	if err == nil {
		if len(decoded) == 0 {
			return nil, fmt.Errorf("%w: empty data", ErrInvalidChunk)
		}
		return decoded, nil
	}
	decoded, rawErr := base64.RawStdEncoding.DecodeString(data)
	if rawErr != nil {
		return nil, fmt.Errorf("%w: %v", ErrInvalidChunk, err)
	}
	if len(decoded) == 0 {
		return nil, fmt.Errorf("%w: empty data", ErrInvalidChunk)
	}
	return decoded, nil
}
