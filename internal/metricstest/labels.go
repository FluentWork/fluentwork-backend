// Package metricstest holds the assertions the /metrics emitters' tests share:
// what a scrape may vary in, and what it may not.
package metricstest

import "strings"

// LabelSetsIn returns the label sets rendered for family, in the order the text
// emitted them, with their values dropped. The values legitimately change
// between two renders — other tests drive the same counters — while the order
// must not, which is what makes two scrapes diffable.
func LabelSetsIn(text, family string) []string {
	var sets []string
	for _, line := range strings.Split(text, "\n") {
		rest, ok := strings.CutPrefix(line, family+"{")
		if !ok {
			continue
		}
		labels, _, ok := strings.Cut(rest, "}")
		if !ok {
			continue
		}
		sets = append(sets, labels)
	}
	return sets
}
