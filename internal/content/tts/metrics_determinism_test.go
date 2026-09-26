package tts

import (
	"strings"
	"testing"
)

// The same state must render the same bytes, so two /metrics scrapes can be
// diffed (BE-S2-6). Unsorted map ranges make the line order vary per scrape.
func TestMetricsRenderingIsReproducible(t *testing.T) {
	const prefix = `tts_route_hits_total{`
	voiceIDs := []string{"zh_female_voicec", "zh_male_voicea", "zh_female_voiceb"}
	for _, voiceID := range voiceIDs {
		processMetrics.incRouteHit(voiceID)
	}

	first := PrometheusMetrics()
	if got := strings.Count(first, prefix); got < len(voiceIDs) {
		t.Fatalf("%s rendered %d label lines, want at least %d; the guard below would pass vacuously", prefix, got, len(voiceIDs))
	}
	for i := 0; i < 16; i++ {
		if got := PrometheusMetrics(); got != first {
			t.Fatalf("render %d differs from render 1:\n--- render 1 ---\n%s\n--- render %d ---\n%s", i+2, first, i+2, got)
		}
	}
}
