package materials

import (
	"strings"
	"testing"
)

// The same state must render the same bytes, so two /metrics scrapes can be
// diffed (BE-S2-6). Unsorted map ranges make the line order vary per scrape.
func TestMetricsRenderingIsReproducible(t *testing.T) {
	const prefix = `refine_status_transition_total{`
	transitions := [][2]string{
		{"failed", "queued"},
		{"processing", "failed"},
		{"queued", "processing"},
	}
	for _, pair := range transitions {
		incTransition(pair[0], pair[1])
	}

	first := PrometheusMetrics()
	if got := strings.Count(first, prefix); got < len(transitions) {
		t.Fatalf("%s rendered %d label lines, want at least %d; the guard below would pass vacuously", prefix, got, len(transitions))
	}
	for i := 0; i < 16; i++ {
		if got := PrometheusMetrics(); got != first {
			t.Fatalf("render %d differs from render 1:\n--- render 1 ---\n%s\n--- render %d ---\n%s", i+2, first, i+2, got)
		}
	}
}
