package httpserver

import (
	"regexp"
	"strings"

	"github.com/FluentWork/fluentwork-backend/internal/account"
	"github.com/FluentWork/fluentwork-backend/internal/content/tts"
	"github.com/FluentWork/fluentwork-backend/internal/corpus"
	"github.com/FluentWork/fluentwork-backend/internal/drill"
	"github.com/FluentWork/fluentwork-backend/internal/materials"
	"github.com/FluentWork/fluentwork-backend/internal/review"
	"github.com/FluentWork/fluentwork-backend/internal/topic"
)

// metricsEmitter is one package's Prometheus exposition text.
type metricsEmitter struct {
	// pkg is the package's directory relative to the module root. It is the key
	// TestEveryMetricsEmitterInTheModuleIsRegistered matches the source tree
	// against, so a new emitter cannot be added without landing here.
	pkg    string
	render func() string
}

// metricsRegistry is the single list of exposition sources GET /metrics serves.
// Registering a package anywhere else leaves its numbers computed but never
// scraped, which nothing else would notice.
var metricsRegistry = []metricsEmitter{
	{pkg: "internal/content/tts", render: tts.PrometheusMetrics},
	{pkg: "internal/corpus", render: corpus.PrometheusMetrics},
	{pkg: "internal/drill", render: drill.PrometheusMetrics},
	{pkg: "internal/account", render: account.PrivacyPrometheusMetrics},
	{pkg: "internal/review", render: review.PrometheusMetrics},
	{pkg: "internal/materials", render: materials.PrometheusMetrics},
	{pkg: "internal/topic", render: topic.PrometheusMetrics},
}

// renderMetrics concatenates every registered emitter in registry order.
func renderMetrics() string {
	var b strings.Builder
	for _, emitter := range metricsRegistry {
		b.WriteString(emitter.render())
	}
	return b.String()
}

// metricSampleLine matches a Prometheus sample or comment line and captures the
// metric family name it belongs to.
var metricSampleLine = regexp.MustCompile(`(?m)^#? ?(?:HELP |TYPE )?([a-zA-Z_:][a-zA-Z0-9_:]*)(\{| )`)

// familiesIn returns every metric family name an exposition text declares,
// keeping duplicates so callers can see repeats.
func familiesIn(text string) []string {
	var names []string
	for _, match := range metricSampleLine.FindAllStringSubmatch(text, -1) {
		names = append(names, match[1])
	}
	return names
}
