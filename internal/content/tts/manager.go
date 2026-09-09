package tts

import (
	"context"
	"sync/atomic"
)

const fallbackTriggerLimit = 3

// Manager wraps primary Volc streaming TTS with duplex fallback.
// After fallbackTriggerLimit consecutive HTTP 5xx from primary, subsequent
// Stream calls go to fallback and tts_fallback_triggered_total is incremented.
type Manager struct {
	primary  Provider
	fallback Provider
	metrics  *Metrics

	fails      atomic.Int32
	fallbackOn atomic.Bool
}

var _ Provider = (*Manager)(nil)

// NewManager constructs a failover Provider. metrics nil uses processMetrics.
func NewManager(primary, fallback Provider) *Manager {
	return &Manager{
		primary:  primary,
		fallback: fallback,
		metrics:  processMetrics,
	}
}

// UsingFallback reports whether traffic has switched to duplex.
func (m *Manager) UsingFallback() bool {
	return m != nil && m.fallbackOn.Load()
}

// Ping probes the active provider.
func (m *Manager) Ping(ctx context.Context) error {
	if m == nil {
		return ErrClosed
	}
	if m.fallbackOn.Load() {
		if m.fallback == nil {
			return ErrClosed
		}
		return m.fallback.Ping(ctx)
	}
	if m.primary == nil {
		return ErrClosed
	}
	return m.primary.Ping(ctx)
}

// Close closes primary and fallback providers.
func (m *Manager) Close() error {
	if m == nil {
		return nil
	}
	var first error
	if m.primary != nil {
		first = m.primary.Close()
	}
	if m.fallback != nil {
		if err := m.fallback.Close(); err != nil && first == nil {
			first = err
		}
	}
	return first
}

// Stream uses primary until consecutive 5xx trips fallback.
func (m *Manager) Stream(ctx context.Context, text string, voice VoiceConfig) (<-chan AudioChunk, error) {
	if m == nil {
		return nil, ErrClosed
	}
	if m.fallbackOn.Load() {
		return m.streamFallback(ctx, text, voice)
	}
	if m.primary == nil {
		return nil, ErrClosed
	}
	ch, err := m.primary.Stream(ctx, text, voice)
	if err == nil {
		m.fails.Store(0)
		return ch, nil
	}
	if !IsHTTP5xx(err) {
		return nil, err
	}
	if m.fails.Add(1) < fallbackTriggerLimit {
		return nil, err
	}
	m.fallbackOn.Store(true)
	m.metrics.incFallback()
	return m.streamFallback(ctx, text, voice)
}

func (m *Manager) streamFallback(ctx context.Context, text string, voice VoiceConfig) (<-chan AudioChunk, error) {
	if m.fallback == nil {
		return nil, ErrClosed
	}
	return m.fallback.Stream(ctx, text, voice)
}
