// Package tts implements B17 TTS: Volc unidirectional streaming plus duplex fallback.
//
// # Status: on, for one caller
//
// The B8 stuck-rescue ladder speaks through this package (docs/92): the voice
// gateway asks app-server to synthesize a rung and pushes the resulting PCM to
// the client as an ai.tts.* stream. The consumer is
// voicegateway.HTTPRescueSynthesizer; the endpoint it calls is
// POST /internal/v1/tts/synthesize.
//
// This paragraph used to read "built, not turned on … POST
// /internal/v1/tts/synthesize has no caller", and that had become false in a way
// worth naming: a stale "nothing calls this" is not a neutral remark, it is a
// wrong answer to the question "does this capability exist?" — it sent a reader
// looking for missing wiring that was already there. TestSynthesizeEndpointHasAConsumer
// now fails when the last caller goes away, so this text and the code fail together.
//
// Still true, and still worth knowing before relying on it:
//
//   - **The remaining callers do not exist.** The two this was originally built
//     for — the iOS flash test and daily-read B20 — are still unbuilt. The daily
//     read's AudioURL is stored and served but nothing generates it, because
//     that needs a fetchable audio path (object storage or an authenticated blob
//     endpoint), which is a delivery decision rather than a synthesis one.
//   - **Prod authorization is unresolved** (meta 77_ P2-4). Development works
//     with the credentials in .env.volc.local; the prod SKU's entitlement is a
//     separate question, and the ladder degrades to text without audio.
//
// Degrading is still the contract: app-server passes nil whenever
// VOLC_SPEECH_API_KEY is empty, and synthesize answers UNAVAILABLE (503) rather
// than panicking. Pinned by TestPostSynthesize_UnconfiguredProviderIsUnavailable.
//
// See docs/92_B8梯子音频投放方案_2026-09-18.md (current) and
// docs/archive/implementation-details/54_B17_TTS_启用决策.md (the earlier
// decision to leave it dormant, and why).
//
// There is no vendor SDK dependency; the HTTP/2 client uses net/http.
package tts

import (
	"context"
	"errors"
	"fmt"
	"strings"
)

// DefaultSpeed is applied when VoiceConfig.Speed is unset or non-positive.
const DefaultSpeed = 1.0

var (
	// ErrEmptyText means Stream was called without synthesizable text.
	ErrEmptyText = errors.New("tts: text is empty")
	// ErrClosed means the provider was already closed.
	ErrClosed = errors.New("tts: provider closed")
	// ErrInvalidChunk means an AudioChunk violates the stream schema.
	ErrInvalidChunk = errors.New("tts: invalid audio chunk")
)

// VoiceConfig selects a synthesis voice and speaking rate.
//
// VoiceID is the Volc speaker id (D-2 catalog in voices.go).
// Speed is a multiplier; 1.0 is the frozen default.
type VoiceConfig struct {
	VoiceID string
	Speed   float64
}

// WithDefaults fills unset VoiceConfig fields without mutating the receiver.
func (v VoiceConfig) WithDefaults() VoiceConfig {
	if v.Speed <= 0 {
		v.Speed = DefaultSpeed
	}
	return v
}

// AudioChunk is one streamed TTS packet.
//
// Seq is 0-based and strictly increasing within a Stream call, matching the
// frozen WSS V2 ai.tts.audio sequence. Data must be non-empty. IsFinal marks
// the last audio packet (ai.tts.end is a separate control frame). DetectedAt
// is unix milliseconds of first-byte availability, used for T-TTS-2 TTFB.
type AudioChunk struct {
	Data       []byte
	Seq        int
	IsFinal    bool
	DetectedAt int64
}

// Validate checks the frozen stream schema for a single chunk.
func (c AudioChunk) Validate() error {
	if c.Seq < 0 {
		return fmt.Errorf("%w: seq %d", ErrInvalidChunk, c.Seq)
	}
	if len(c.Data) == 0 {
		return fmt.Errorf("%w: empty data at seq %d", ErrInvalidChunk, c.Seq)
	}
	if c.DetectedAt < 0 {
		return fmt.Errorf("%w: detected_at %d", ErrInvalidChunk, c.DetectedAt)
	}
	return nil
}

// Provider synthesizes text to a stream of audio chunks.
//
// Stream must close the returned channel when the stream ends, the context
// is cancelled, or the provider is closed. Ping is a liveness probe for
// fallback switching (T-TTS-3 / T-TTS-5). Close is idempotent.
type Provider interface {
	Stream(ctx context.Context, text string, voice VoiceConfig) (<-chan AudioChunk, error)
	Ping(ctx context.Context) error
	Close() error
}

// NormalizeStreamInput trims synthesizable text and applies VoiceConfig defaults.
func NormalizeStreamInput(text string, voice VoiceConfig) (string, VoiceConfig, error) {
	text = strings.TrimSpace(text)
	if text == "" {
		return "", VoiceConfig{}, ErrEmptyText
	}
	return text, voice.WithDefaults(), nil
}
