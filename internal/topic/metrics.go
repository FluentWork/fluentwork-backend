package topic

import (
	"fmt"
	"strings"
	"sync"
	"sync/atomic"
)

var (
	generatedOK   atomic.Int64
	generatedFail atomic.Int64
	parseErrors   atomic.Int64
	checkins      atomic.Int64
	skipMu        sync.Mutex
	skips         = map[string]int64{}
)

func incGenerated(ok bool) {
	if ok {
		generatedOK.Add(1)
		return
	}
	generatedFail.Add(1)
}

func incParseError() { parseErrors.Add(1) }
func incCheckin()    { checkins.Add(1) }

func incSkip(reason string) {
	skipMu.Lock()
	skips[reason]++
	skipMu.Unlock()
}

// PrometheusMetrics renders B23 counters.
func PrometheusMetrics() string {
	var b strings.Builder
	b.WriteString("# HELP topic_card_generated_total Topic card daily generation outcomes.\n")
	b.WriteString("# TYPE topic_card_generated_total counter\n")
	fmt.Fprintf(&b, "topic_card_generated_total{status=\"success\"} %d\n", generatedOK.Load())
	fmt.Fprintf(&b, "topic_card_generated_total{status=\"fail\"} %d\n", generatedFail.Load())
	b.WriteString("# HELP topic_card_parse_error_total Times topic-card LLM JSON parsing failed.\n")
	b.WriteString("# TYPE topic_card_parse_error_total counter\n")
	fmt.Fprintf(&b, "topic_card_parse_error_total %d\n", parseErrors.Load())
	b.WriteString("# HELP topic_card_gen_skipped_total Users skipped during daily generation.\n")
	b.WriteString("# TYPE topic_card_gen_skipped_total counter\n")
	skipMu.Lock()
	if len(skips) == 0 {
		b.WriteString("topic_card_gen_skipped_total{reason=\"none\"} 0\n")
	} else {
		for reason, n := range skips {
			fmt.Fprintf(&b, "topic_card_gen_skipped_total{reason=%q} %d\n", reason, n)
		}
	}
	skipMu.Unlock()
	b.WriteString("# HELP topic_card_checkin_total Successful topic card checkins.\n")
	b.WriteString("# TYPE topic_card_checkin_total counter\n")
	fmt.Fprintf(&b, "topic_card_checkin_total %d\n", checkins.Load())
	return b.String()
}
