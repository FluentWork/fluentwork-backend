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
)

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
		return b.String()
	}
	keys := make([]string, 0, len(deprecatedPostFavoriteByUA))
	for key := range deprecatedPostFavoriteByUA {
		keys = append(keys, key)
	}
	sort.Strings(keys)
	for _, key := range keys {
		fmt.Fprintf(&b, "corpus_deprecated_post_favorite_total{user_agent=%q} %d\n", key, deprecatedPostFavoriteByUA[key])
	}
	return b.String()
}
