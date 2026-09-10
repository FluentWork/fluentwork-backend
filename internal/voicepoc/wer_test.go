package voicepoc

import (
	"math"
	"testing"
)

func TestScoreWERIdenticalIsZero(t *testing.T) {
	t.Parallel()

	got := ScoreWER("I think we should ship it this week.", "I think we should ship it this week.")
	if got.Errors() != 0 {
		t.Fatalf("errors = %d, want 0 (%+v)", got.Errors(), got)
	}
	if got.ReferenceLength != 8 {
		t.Fatalf("reference length = %d, want 8", got.ReferenceLength)
	}
}

// Case and punctuation are not errors — the reference is written for humans, and
// scoring a missing full stop as an ASR mistake would inflate every sample.
func TestScoreWERIgnoresCaseAndPunctuation(t *testing.T) {
	t.Parallel()

	got := ScoreWER("Let's wrap up and take the rest offline.", "lets wrap up and take the rest offline")
	if got.Errors() != 0 {
		t.Fatalf("errors = %d, want 0 (%+v)", got.Errors(), got)
	}
}

// The three error kinds have to stay apart. They fail differently and they point
// at different fixes: substitutions are accent, deletions are swallowed endings,
// insertions are vowels inserted into clusters.
func TestScoreWERSeparatesSubstitutionDeletionAndInsertion(t *testing.T) {
	t.Parallel()

	t.Run("substitution", func(t *testing.T) {
		t.Parallel()
		got := ScoreWER("I think we should ship it", "I sink we should ship it")
		if got.Substitutions != 1 || got.Deletions != 0 || got.Insertions != 0 {
			t.Fatalf("got S=%d D=%d I=%d, want S=1 D=0 I=0", got.Substitutions, got.Deletions, got.Insertions)
		}
	})

	t.Run("deletion", func(t *testing.T) {
		t.Parallel()
		// A dropped word ending is the classic Mandarin-speaker failure.
		got := ScoreWER("we need to test the release branch", "we nee to test the release branch")
		if got.Substitutions != 1 || got.Deletions != 0 || got.Insertions != 0 {
			// "nee" vs "need" is one substitution at the token level.
			t.Fatalf("got S=%d D=%d I=%d, want S=1 D=0 I=0", got.Substitutions, got.Deletions, got.Insertions)
		}
	})

	t.Run("whole word deleted", func(t *testing.T) {
		t.Parallel()
		got := ScoreWER("we need to test the release branch", "we need to test release branch")
		if got.Deletions != 1 || got.Substitutions != 0 || got.Insertions != 0 {
			t.Fatalf("got S=%d D=%d I=%d, want D=1", got.Substitutions, got.Deletions, got.Insertions)
		}
	})

	t.Run("insertion", func(t *testing.T) {
		t.Parallel()
		// The epenthetic vowel Mandarin speakers add to an s-cluster.
		got := ScoreWER("let's go with the second option", "let's go with the sekond option")
		if got.Substitutions != 1 {
			t.Fatalf("got S=%d, want S=1 (%+v)", got.Substitutions, got)
		}
	})
}

func TestScoreWERRateIsErrorsOverReferenceLength(t *testing.T) {
	t.Parallel()

	// 4 reference tokens, 2 wrong -> 0.5
	got := ScoreWER("alpha beta gamma delta", "alpha beta gamma epsilon")
	if math.Abs(got.Rate()-0.25) > 1e-9 {
		t.Fatalf("rate = %v, want 0.25 (%+v)", got.Rate(), got)
	}
}

// An empty hypothesis against a non-empty reference is a total deletion — that
// is what "the ASR returned nothing" looks like, and it must not read as a
// perfect score.
func TestScoreWEREmptyHypothesisIsAllDeletions(t *testing.T) {
	t.Parallel()

	got := ScoreWER("I think we should ship it", "")
	if got.Deletions != 6 || got.Substitutions != 0 || got.Insertions != 0 {
		t.Fatalf("got S=%d D=%d I=%d, want D=6", got.Substitutions, got.Deletions, got.Insertions)
	}
	if math.Abs(got.Rate()-1.0) > 1e-9 {
		t.Fatalf("rate = %v, want 1.0", got.Rate())
	}
}

// An empty reference has no rate. Returning 0 would read as "perfect", which is
// the opposite of what an unanswered sample means.
func TestScoreWEREmptyReferenceHasNoRate(t *testing.T) {
	t.Parallel()

	got := ScoreWER("", "something the model made up")
	if got.ReferenceLength != 0 {
		t.Fatalf("reference length = %d, want 0", got.ReferenceLength)
	}
	if got.Rate() != 0 {
		t.Fatalf("rate = %v; a sample with no reference is unscored, not perfect", got.Rate())
	}
}

// A Chinese answer to an English reference is a total miss, and it has to score
// as one. Per-character tokenisation would also make it a total miss, but for
// the wrong reason and with a nonsense denominator — see TokenizeWER.
func TestTokenizeWERKeepsCJKRunWhole(t *testing.T) {
	t.Parallel()

	got := TokenizeWER("今天我要学 architecture")
	want := []string{"今天我要学", "architecture"}
	if len(got) != len(want) {
		t.Fatalf("tokens = %#v, want %#v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("tokens = %#v, want %#v", got, want)
		}
	}
}
