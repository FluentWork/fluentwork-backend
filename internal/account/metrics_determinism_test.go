package account

import (
	"strings"
	"testing"
)

// The same state must render the same bytes, so two /metrics scrapes can be
// diffed (BE-S2-6). Unsorted map ranges make the line order vary per scrape.
func TestMetricsRenderingIsReproducible(t *testing.T) {
	const prefix = `tombstone_inserted_total{`
	entityTypes := []string{"utterance", "material", "session"}
	for _, entityType := range entityTypes {
		incTombstone(entityType)
	}

	first := PrivacyPrometheusMetrics()
	if got := strings.Count(first, prefix); got < len(entityTypes) {
		t.Fatalf("%s rendered %d label lines, want at least %d; the guard below would pass vacuously", prefix, got, len(entityTypes))
	}
	for i := 0; i < 16; i++ {
		if got := PrivacyPrometheusMetrics(); got != first {
			t.Fatalf("render %d differs from render 1:\n--- render 1 ---\n%s\n--- render %d ---\n%s", i+2, first, i+2, got)
		}
	}
}
