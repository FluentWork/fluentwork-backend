package materials

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
)

var (
	timeouts    atomic.Int64
	parseErrors atomic.Int64
	transMu     sync.Mutex
	transitions = map[string]int64{}
)

func incTimeout()    { timeouts.Add(1) }
func incParseError() { parseErrors.Add(1) }

func incTransition(from, to string) {
	key := from + "->" + to
	transMu.Lock()
	transitions[key]++
	transMu.Unlock()
}

// PrometheusMetrics renders B21 counters. Names are material_* so they do not
// collide with drill's refine_timeout_total on the same /metrics scrape.
func PrometheusMetrics() string {
	var b strings.Builder
	b.WriteString("# HELP material_refine_timeout_total Times material refine LLM calls timed out or failed.\n")
	b.WriteString("# TYPE material_refine_timeout_total counter\n")
	fmt.Fprintf(&b, "material_refine_timeout_total %d\n", timeouts.Load())
	b.WriteString("# HELP material_refine_parse_error_total Times material refine JSON parsing failed.\n")
	b.WriteString("# TYPE material_refine_parse_error_total counter\n")
	fmt.Fprintf(&b, "material_refine_parse_error_total %d\n", parseErrors.Load())
	b.WriteString("# HELP refine_status_transition_total Material refine_status changes.\n")
	b.WriteString("# TYPE refine_status_transition_total counter\n")
	transMu.Lock()
	defer transMu.Unlock()
	if len(transitions) == 0 {
		b.WriteString("refine_status_transition_total{from=\"none\",to=\"none\"} 0\n")
		return b.String()
	}
	for key, n := range transitions {
		parts := strings.SplitN(key, "->", 2)
		from, to := "unknown", "unknown"
		if len(parts) == 2 {
			from, to = parts[0], parts[1]
		}
		fmt.Fprintf(&b, "refine_status_transition_total{from=%q,to=%q} %d\n", from, to, n)
	}
	return b.String()
}
