package topic

import (
	"strings"
	"testing"
)

// The same state must render the same bytes, so two /metrics scrapes can be
// diffed (BE-S2-6). This half of PrometheusMetrics was left unsorted while the
// dismissals map next to it was sorted.
func TestMetricsRenderingIsReproducible(t *testing.T) {
	const prefix = `topic_card_gen_skipped_total{`
	reasons := []string{"no_corpus", "quota_exhausted", "below_threshold"}
	for _, reason := range reasons {
		incSkip(reason)
	}

	first := PrometheusMetrics()
	if got := strings.Count(first, prefix); got < len(reasons) {
		t.Fatalf("%s rendered %d label lines, want at least %d; the guard below would pass vacuously", prefix, got, len(reasons))
	}
	for i := 0; i < 16; i++ {
		if got := PrometheusMetrics(); got != first {
			t.Fatalf("render %d differs from render 1:\n--- render 1 ---\n%s\n--- render %d ---\n%s", i+2, first, i+2, got)
		}
	}
}
