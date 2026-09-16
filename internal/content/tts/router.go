package tts

import (
	"context"
)

// Router routes TTS requests to different providers based on voice_id.
// Unmatched voice_id requests fall back to a default provider.
type Router struct {
	routes   map[string]Provider
	fallback Provider
	metrics  *Metrics
}

var _ Provider = (*Router)(nil)

// NewRouter creates a Router with voice-specific providers and a fallback.
// routes maps voice_id to Provider. fallback handles unmatched voice_id.
// Both may be nil; nil fallback returns ErrClosed for unmatched requests.
func NewRouter(routes map[string]Provider, fallback Provider) *Router {
	if routes == nil {
		routes = make(map[string]Provider)
	}
	return &Router{
		routes:   routes,
		fallback: fallback,
		metrics:  processMetrics,
	}
}

// Stream routes to the provider matching voice.VoiceID, or fallback.
func (r *Router) Stream(ctx context.Context, text string, voice VoiceConfig) (<-chan AudioChunk, error) {
	if r == nil {
		return nil, ErrClosed
	}

	provider := r.routes[voice.VoiceID]
	if provider == nil {
		if r.fallback == nil {
			r.metrics.incRouteMiss(voice.VoiceID)
			return nil, ErrClosed
		}
		r.metrics.incRouteMiss(voice.VoiceID)
		return r.fallback.Stream(ctx, text, voice)
	}

	r.metrics.incRouteHit(voice.VoiceID)
	return provider.Stream(ctx, text, voice)
}

// Ping probes all registered providers and the fallback.
// Returns the first error encountered, or nil if all succeed.
func (r *Router) Ping(ctx context.Context) error {
	if r == nil {
		return ErrClosed
	}

	for _, p := range r.routes {
		if p != nil {
			if err := p.Ping(ctx); err != nil {
				return err
			}
		}
	}

	if r.fallback != nil {
		return r.fallback.Ping(ctx)
	}

	return nil
}

// Close closes all registered providers and the fallback.
// Returns the first error encountered.
func (r *Router) Close() error {
	if r == nil {
		return nil
	}

	var firstErr error

	for _, p := range r.routes {
		if p != nil {
			if err := p.Close(); err != nil && firstErr == nil {
				firstErr = err
			}
		}
	}

	if r.fallback != nil {
		if err := r.fallback.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
	}

	return firstErr
}
