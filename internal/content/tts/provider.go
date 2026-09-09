// Package tts defines the B17 TTS Provider contract.
//
// Implementations (volc streaming, duplex fallback) land in later T-TTS tickets.
// This package has no vendor SDK dependency.
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
