package review

import (
	"fmt"
	"strings"
	"sync/atomic"
)

var (
	timeouts    atomic.Int64
	parseErrors atomic.Int64
)

func incTimeout()    { timeouts.Add(1) }
func incParseError() { parseErrors.Add(1) }

// PrometheusMetrics renders B18 counters for GET /metrics.
func PrometheusMetrics() string {
	var b strings.Builder
	b.WriteString("# HELP review_eval_timeout_total Times utterance eval LLM calls timed out or failed.\n")
	b.WriteString("# TYPE review_eval_timeout_total counter\n")
	fmt.Fprintf(&b, "review_eval_timeout_total %d\n", timeouts.Load())
	b.WriteString("# HELP review_eval_parse_error_total Times utterance eval JSON parsing failed.\n")
	b.WriteString("# TYPE review_eval_parse_error_total counter\n")
	fmt.Fprintf(&b, "review_eval_parse_error_total %d\n", parseErrors.Load())
	return b.String()
}
