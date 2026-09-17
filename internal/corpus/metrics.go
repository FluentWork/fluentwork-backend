package corpus

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

const (
	// PostFavoriteSunsetHTTPDate is the RFC 1123 Sunset header for the deprecated
	// POST /corpus/blocks/:id/favorite route (removed in V1.5).
	PostFavoriteSunsetHTTPDate = "Thu, 01 Oct 2026 00:00:00 GMT"
)

var (
	deprecatedPostFavoriteTotal atomic.Int64
	deprecatedPostFavoriteMu    sync.Mutex
	deprecatedPostFavoriteByUA  = map[string]int64{}

	feedbackMu    sync.Mutex
	feedbackByWhy = map[string]int64{}
)

// incFeedback counts one first-time quality signal, by reason. This counter is
// 83_ §2.2 风险 1's measure: a rate that never falls means the rewrite rules
// have not landed.
func incFeedback(reason string) {
	feedbackMu.Lock()
	feedbackByWhy[reason]++
	feedbackMu.Unlock()
}

// IncDeprecatedPostFavorite increments corpus_deprecated_post_favorite_total.
func IncDeprecatedPostFavorite(userAgent string) {
	key := metricUserAgent(userAgent)
	deprecatedPostFavoriteMu.Lock()
	deprecatedPostFavoriteByUA[key]++
	deprecatedPostFavoriteMu.Unlock()
	deprecatedPostFavoriteTotal.Add(1)
}

// DeprecatedPostFavoriteTotal returns the process counter for tests and /metrics.
func DeprecatedPostFavoriteTotal() int64 {
	return deprecatedPostFavoriteTotal.Load()
}

func metricUserAgent(raw string) string {
	ua := strings.ToLower(strings.TrimSpace(raw))
	switch {
	case ua == "":
		return "unknown"
	case strings.Contains(ua, "iphone"), strings.Contains(ua, "ios"), strings.Contains(ua, "fluentwork"):
		return "ios"
	default:
		return "other"
	}
}

// PrometheusMetrics renders corpus counters in Prometheus text format.
func PrometheusMetrics() string {
	deprecatedPostFavoriteMu.Lock()
	defer deprecatedPostFavoriteMu.Unlock()
	var b strings.Builder
	b.WriteString("# HELP corpus_deprecated_post_favorite_total Times clients called deprecated POST /corpus/blocks/:id/favorite.\n")
	b.WriteString("# TYPE corpus_deprecated_post_favorite_total counter\n")
	if len(deprecatedPostFavoriteByUA) == 0 {
		fmt.Fprintf(&b, "corpus_deprecated_post_favorite_total{user_agent=%q} 0\n", "unknown")
	} else {
		keys := make([]string, 0, len(deprecatedPostFavoriteByUA))
		for key := range deprecatedPostFavoriteByUA {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			fmt.Fprintf(&b, "corpus_deprecated_post_favorite_total{user_agent=%q} %d\n", key, deprecatedPostFavoriteByUA[key])
		}
	}
	feedbackMu.Lock()
	defer feedbackMu.Unlock()
	b.WriteString("# HELP corpus_block_feedback_total Refine quality signals, by reason (83_ §2.2).\n")
	b.WriteString("# TYPE corpus_block_feedback_total counter\n")
	if len(feedbackByWhy) == 0 {
		b.WriteString("corpus_block_feedback_total{reason=\"none\"} 0\n")
		return b.String()
	}
	reasons := make([]string, 0, len(feedbackByWhy))
	for reason := range feedbackByWhy {
		reasons = append(reasons, reason)
	}
	sort.Strings(reasons)
	for _, reason := range reasons {
		fmt.Fprintf(&b, "corpus_block_feedback_total{reason=%q} %d\n", reason, feedbackByWhy[reason])
	}
	return b.String()
}
