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
	defaultVolcTTSSpeaker    = "zh_female_vv_jupiter_bigtts"
	defaultVolcTTSFormat     = "ogg_opus"
	defaultVolcTTSSampleRate = 24000
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
