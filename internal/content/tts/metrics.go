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
}

func (m *Metrics) incFallback() {
	if m != nil {
		m.fallbackTriggered.Add(1)
	}
}

// FallbackTriggered returns tts_fallback_triggered_total.
func (m *Metrics) FallbackTriggered() int64 {
	if m == nil {
		return 0
	}
	return m.fallbackTriggered.Load()
}

var processMetrics = &Metrics{}

// PrometheusMetrics renders the TTS counters in Prometheus text format.
func PrometheusMetrics() string {
	return fmt.Sprintf(
		"# HELP tts_fallback_triggered_total Times TTS switched from volc streaming to duplex fallback after consecutive HTTP 5xx.\n"+
			"# TYPE tts_fallback_triggered_total counter\n"+
			"tts_fallback_triggered_total{from=%q,to=%q} %d\n",
		metricFallbackFrom,
		metricFallbackTo,
		processMetrics.FallbackTriggered(),
	)
}
