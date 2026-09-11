package voicepoc

import "testing"

// The seven formatting differences measured in docs/52 §2 — every one of them
// was counted as an error, and none of them is the recogniser mishearing a word.
//
//	said          reference
//	3:30          three thirty
//	3 years       three years
//	I have been   I've been
//	I will        I'll
//	there is      there's
//	clean up      cleanup
//	back end      backend
//
// Normalising them moves the 30-sample run from 14.7% to 6.4% (18 errors).
// Both numbers are reported; neither replaces the other.
//
// `docs/52` recorded the normalised figure as 10.4%, which **does not
// reproduce** — see TestRecordedSampleRunRescoresToTheDocumentedBaseline.
func TestScoreWERNormalizedCollapsesTheMeasuredFormattingDifferences(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		said      string
		reference string
	}{
		{"time", "3:30", "three thirty"},
		{"count", "3 years", "three years"},
		{"contraction have", "I have been", "I've been"},
		{"contraction will", "I will", "I'll"},
		{"contraction is", "there is", "there's"},
		{"compound cleanup", "clean up", "cleanup"},
		{"compound backend", "back end", "backend"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			// Sanity: the raw scorer really does count these, which is what makes
			// the normalised result the interesting one.
			if raw := ScoreWER(c.reference, c.said); raw.Errors() == 0 {
				t.Fatalf("expected the raw scorer to count %q vs %q as an error", c.said, c.reference)
			}

			got := ScoreWERNormalized(c.reference, c.said)
			if got.Errors() != 0 {
				t.Errorf("normalised score still counts %d error(s) for %q vs %q",
					got.Errors(), c.said, c.reference)
			}
		})
	}
}

// The boundary: normalisation must not swallow genuine mishearings.
//
// This is the guard that keeps the rule "only surface forms" from quietly
// becoming "anything goes". Every pair here is a real substitution from the
// measured run — the recogniser heard a different word — and every one must
// still score.
func TestScoreWERNormalizedStillCountsRealSubstitutions(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name      string
		said      string
		reference string
	}{
		{"blocked/broke", "blocked", "broke"},
		{"pair/peel", "pair", "peel"},
		{"cache/catch", "cache", "catch"},
		{"dropped word", "we ship it", "we should ship it"},
		{"invented word", "we should um ship it", "we should ship it"},
		// A number word against a *different* number is a mishearing, not a
		// spelling: normalisation must not make all numerals equivalent.
		{"different number", "three years", "five years"},
		// `its` is not the contraction `it's`.
		{"possessive is not a contraction", "its", "it is"},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			t.Parallel()

			if got := ScoreWERNormalized(c.reference, c.said); got.Errors() == 0 {
				t.Errorf("normalisation swallowed a real difference: %q vs %q", c.said, c.reference)
			}
		})
	}
}
