package topic

import (
	"fmt"
	"sort"
	"strings"
	"sync"
	"sync/atomic"
)

var (
	generatedOK   atomic.Int64
	generatedFail atomic.Int64
	parseErrors   atomic.Int64
	checkins      atomic.Int64
	ungrounded    atomic.Int64
	dismissMu     sync.Mutex
	dismissals    = map[string]int64{}
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

// incUngrounded counts cards dropped by H2's grounding check — the direct
// measure of how often the model proposes a topic the learner's corpus cannot
// serve (PRD §7.8: 禁止泛话题).
func incUngrounded() { ungrounded.Add(1) }

// incDismiss counts why cards went unused, by reason. This is the only negative
// signal about 实战出口 the product can collect (86_ M11).
func incDismiss(reason string) {
	dismissMu.Lock()
	dismissals[reason]++
	dismissMu.Unlock()
}

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
	b.WriteString("# HELP topic_card_ungrounded_total Cards dropped because no block of the learner's could serve them.\n")
	b.WriteString("# TYPE topic_card_ungrounded_total counter\n")
	fmt.Fprintf(&b, "topic_card_ungrounded_total %d\n", ungrounded.Load())
	b.WriteString("# HELP topic_card_dismissed_total Cards marked as not acted on, by reason.\n")
	b.WriteString("# TYPE topic_card_dismissed_total counter\n")
	dismissMu.Lock()
	if len(dismissals) == 0 {
		b.WriteString("topic_card_dismissed_total{reason=\"none\"} 0\n")
	} else {
		reasons := make([]string, 0, len(dismissals))
		for reason := range dismissals {
			reasons = append(reasons, reason)
		}
		sort.Strings(reasons)
		for _, reason := range reasons {
			fmt.Fprintf(&b, "topic_card_dismissed_total{reason=%q} %d\n", reason, dismissals[reason])
		}
	}
	dismissMu.Unlock()
	b.WriteString("# HELP topic_card_checkin_total Successful topic card checkins.\n")
	b.WriteString("# TYPE topic_card_checkin_total counter\n")
	fmt.Fprintf(&b, "topic_card_checkin_total %d\n", checkins.Load())
	return b.String()
}
