package topic

import (
	"slices"
	"testing"

	"github.com/FluentWork/fluentwork-backend/internal/metricstest"
)

// A scrape of one state must order its label sets the same way every time, so
// two scrapes can be diffed (BE-S2-6). This half of PrometheusMetrics was left
// unsorted while the dismissals map next to it was sorted. Counting is not part
// of the check: the values may change between renders.
func TestMetricsRenderingIsReproducible(t *testing.T) {
	const family = "topic_card_gen_skipped_total"
	for _, reason := range []string{"no_corpus", "quota_exhausted", "below_threshold"} {
		incSkip(reason)
	}

	// Eight renders, because a single map order can land sorted by luck.
	for i := 0; i < 8; i++ {
		sets := metricstest.LabelSetsIn(PrometheusMetrics(), family)
		if len(sets) < 3 {
			t.Fatalf("render %d listed %d label sets of %s, want at least 3", i+1, len(sets), family)
		}
		if !slices.IsSorted(sets) {
			t.Fatalf("render %d listed %s label sets out of order: %v", i+1, family, sets)
		}
	}
}
