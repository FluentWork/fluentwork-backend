package main

import (
	"encoding/json"
	"fmt"
	"os"
	"sort"
	"strings"
)

// A baseline is the run a change is measured against. Without one, every
// evaluation is an anecdote: "the judge feels better today" is not a review
// comment anyone can act on.
//
// The baseline stores each metric's value *and* the drift it tolerates, because
// these numbers move on their own — a model replays a prompt slightly
// differently each run, and a gate that fires on that noise gets ignored, which
// is worse than no gate.
type baseline struct {
	Version    int                       `json:"version"`
	Note       string                    `json:"note"`
	RecordedAt string                    `json:"recorded_at"`
	Metrics    map[string]baselineMetric `json:"metrics"`
}

type baselineMetric struct {
	Value     float64 `json:"value"`
	Target    string  `json:"target"`
	Tolerance float64 `json:"tolerance"`
	// Gated is false for metrics that are reported but not enforced
	// (informational ones like paraphrase recall, which the product trades away
	// on purpose).
	Gated bool `json:"gated"`
}

// tolerances are per metric family: a rate may wobble by two points, a 1–5 mean
// by two tenths. Anything tighter fires on sampling noise.
const (
	rateTolerance = 0.02
	meanTolerance = 0.2
)

// toleranceFor picks the drift a metric is allowed before it counts as a
// regression.
func toleranceFor(m metric) float64 {
	switch {
	case informationalMetrics[m.Name]:
		return 0 // informational: never gates
	case strings.HasSuffix(m.Name, "_mean"):
		return meanTolerance
	case strings.HasSuffix(m.Name, "_rate"):
		return rateTolerance
	default:
		return rateTolerance
	}
}

// informationalMetrics are reported but never gate a change.
//
//   - hit_paraphrase_recall: the detector trades recall for precision on purpose
//     (PRD §5.2.3), so a drop is not a regression.
//   - topic_generic_rate / topic_grounded_mean: the referee's judgement of
//     topic quality moved 0.556 → 0.444 → 0.000 → 0.333 across four defensible
//     measurement designs (87_ §7.7). A number that unstable cannot gate
//     anything; the *structural* topic metrics stay gated because they are
//     deterministic (cards produced, each with blocks and a source).
var informationalMetrics = map[string]bool{
	"hit_paraphrase_recall": true,
	"topic_generic_rate":    true,
	"topic_grounded_mean":   true,
}

func gatedMetric(m metric) bool {
	return !informationalMetrics[m.Name]
}

func baselineFrom(rep report) baseline {
	out := baseline{
		Version:    1,
		RecordedAt: rep.RunID,
		Note:       "written by cmd/eval-moat-flow --write-baseline; regenerate after an intentional change",
		Metrics:    map[string]baselineMetric{},
	}
	for _, m := range rep.Metrics {
		out.Metrics[m.Name] = baselineMetric{
			Value:     m.Value,
			Target:    m.Target,
			Tolerance: toleranceFor(m),
			Gated:     gatedMetric(m),
		}
	}
	return out
}

func writeBaseline(path string, rep report) error {
	raw, err := json.MarshalIndent(baselineFrom(rep), "", "  ")
	if err != nil {
		return err
	}
	return os.WriteFile(path, append(raw, '\n'), 0o644)
}

func loadBaseline(path string) (baseline, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return baseline{}, fmt.Errorf("read baseline: %w", err)
	}
	var b baseline
	if err := json.Unmarshal(raw, &b); err != nil {
		return baseline{}, fmt.Errorf("parse baseline: %w", err)
	}
	return b, nil
}

// regression is one metric that moved the wrong way by more than its tolerance.
type regression struct {
	Name      string  `json:"name"`
	Baseline  float64 `json:"baseline"`
	Current   float64 `json:"current"`
	Delta     float64 `json:"delta"`
	Tolerance float64 `json:"tolerance"`
	Target    string  `json:"target"`
}

// compareToBaseline returns the metrics that regressed, and a human-readable
// table of every gated metric for the report.
func compareToBaseline(base baseline, rep report) ([]regression, string) {
	var out []regression
	var b strings.Builder
	b.WriteString("| 指标 | 基线 | 本次 | 变化 | 容忍 | 结论 |\n|---|---|---|---|---|---|\n")
	names := make([]string, 0, len(rep.Metrics))
	for _, m := range rep.Metrics {
		names = append(names, m.Name)
	}
	sort.Strings(names)
	for _, name := range names {
		var current float64
		for _, m := range rep.Metrics {
			if m.Name == name {
				current = m.Value
			}
		}
		entry, ok := base.Metrics[name]
		if !ok {
			fmt.Fprintf(&b, "| %s | — | %.3f | 新增 | — | 无基线 |\n", name, current)
			continue
		}
		delta := current - entry.Value
		verdict := "持平"
		if !entry.Gated {
			verdict = "仅记录"
		} else if worseBy(entry, delta) > entry.Tolerance {
			verdict = "**退化**"
			out = append(out, regression{
				Name: name, Baseline: entry.Value, Current: current,
				Delta: delta, Tolerance: entry.Tolerance, Target: entry.Target,
			})
		} else if betterBy(entry, delta) > entry.Tolerance {
			verdict = "改善"
		}
		fmt.Fprintf(&b, "| %s | %.3f | %.3f | %+.3f | %.2f | %s |\n",
			name, entry.Value, current, delta, entry.Tolerance, verdict)
	}
	return out, b.String()
}

// worseBy is how far a metric moved in the direction it should not.
func worseBy(entry baselineMetric, delta float64) float64 {
	if strings.HasPrefix(entry.Target, "<=") {
		return delta // lower is better
	}
	return -delta // higher is better
}

func betterBy(entry baselineMetric, delta float64) float64 {
	if strings.HasPrefix(entry.Target, "<=") {
		return -delta
	}
	return delta
}
