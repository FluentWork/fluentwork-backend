package tts

import (
	"fmt"
	"sync/atomic"
)

const (
	metricFallbackFrom = "volc_streaming"
	metricFallbackTo   = "volc_duplex"
)

// Metrics holds process-local TTS counters exposed on GET /metrics.
type Metrics struct {
	fallbackTriggered atomic.Int64
	routeHits         map[string]*atomic.Int64
	routeMisses       atomic.Int64
}

func (m *Metrics) incFallback() {
	if m != nil {
		m.fallbackTriggered.Add(1)
	}
}

func (m *Metrics) incRouteHit(voiceID string) {
	if m == nil {
		return
	}
	if m.routeHits == nil {
		m.routeHits = make(map[string]*atomic.Int64)
	}
	counter, ok := m.routeHits[voiceID]
	if !ok {
		counter = &atomic.Int64{}
		m.routeHits[voiceID] = counter
	}
	counter.Add(1)
}

func (m *Metrics) incRouteMiss(_ string) {
	if m != nil {
		m.routeMisses.Add(1)
	}
}

// FallbackTriggered returns tts_fallback_triggered_total.
func (m *Metrics) FallbackTriggered() int64 {
	if m == nil {
		return 0
	}
	return m.fallbackTriggered.Load()
}

// RouteHits returns the hit count for a specific voice_id.
func (m *Metrics) RouteHits(voiceID string) int64 {
	if m == nil || m.routeHits == nil {
		return 0
	}
	counter, ok := m.routeHits[voiceID]
	if !ok {
		return 0
	}
	return counter.Load()
}

// RouteMisses returns tts_route_misses_total.
func (m *Metrics) RouteMisses() int64 {
	if m == nil {
		return 0
	}
	return m.routeMisses.Load()
}

var processMetrics = &Metrics{
	routeHits: make(map[string]*atomic.Int64),
}

// PrometheusMetrics renders the TTS counters in Prometheus text format.
func PrometheusMetrics() string {
	result := fmt.Sprintf(
		"# HELP tts_fallback_triggered_total Times TTS switched from volc streaming to duplex fallback after consecutive HTTP 5xx.\n"+
			"# TYPE tts_fallback_triggered_total counter\n"+
			"tts_fallback_triggered_total{from=%q,to=%q} %d\n",
		metricFallbackFrom,
		metricFallbackTo,
		processMetrics.FallbackTriggered(),
	)

	result += "# HELP tts_route_hits_total TTS requests routed to a matching voice_id provider.\n" +
		"# TYPE tts_route_hits_total counter\n"
	for voiceID, counter := range processMetrics.routeHits {
		result += fmt.Sprintf("tts_route_hits_total{voice_id=%q} %d\n", voiceID, counter.Load())
	}

	result += fmt.Sprintf(
		"# HELP tts_route_misses_total TTS requests with no matching voice_id, using fallback.\n"+
			"# TYPE tts_route_misses_total counter\n"+
			"tts_route_misses_total %d\n",
		processMetrics.RouteMisses(),
	)

	return result
}
