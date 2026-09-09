package drill

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
)

var (
	parseErrors  atomic.Int64
	judgeErrors  atomic.Int64
	transitionMu sync.Mutex
	transitions  = map[string]int64{}
)

func incParseError() { parseErrors.Add(1) }
func incJudgeError() { judgeErrors.Add(1) }

func incStateTransition(from, to string) {
	key := from + "->" + to
	transitionMu.Lock()
	transitions[key]++
	transitionMu.Unlock()
}

// PrometheusMetrics renders drill counters.
func PrometheusMetrics() string {
	var b strings.Builder
	b.WriteString("# HELP refine_parse_error_total Drill/judge JSON parse failures.\n")
	b.WriteString("# TYPE refine_parse_error_total counter\n")
	fmt.Fprintf(&b, "refine_parse_error_total %d\n", parseErrors.Load())
	b.WriteString("# HELP refine_timeout_total Drill/judge LLM timeouts or call failures.\n")
	b.WriteString("# TYPE refine_timeout_total counter\n")
	fmt.Fprintf(&b, "refine_timeout_total %d\n", judgeErrors.Load())
	b.WriteString("# HELP drill_state_transition_total SM-2 state changes after a judge.\n")
	b.WriteString("# TYPE drill_state_transition_total counter\n")
	transitionMu.Lock()
	defer transitionMu.Unlock()
	if len(transitions) == 0 {
		b.WriteString("drill_state_transition_total{from=\"none\",to=\"none\"} 0\n")
		return b.String()
	}
	keys := make([]string, 0, len(transitions))
	for k := range transitions {
		keys = append(keys, k)
	}
	// stable-ish: unsorted ok for tests Contains
	for _, key := range keys {
		parts := strings.SplitN(key, "->", 2)
		from, to := "unknown", "unknown"
		if len(parts) == 2 {
			from, to = parts[0], parts[1]
		}
		fmt.Fprintf(&b, "drill_state_transition_total{from=%q,to=%q} %d\n", from, to, transitions[key])
	}
	return b.String()
}
