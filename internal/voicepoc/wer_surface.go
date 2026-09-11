package voicepoc

import (
	"regexp"
	"strconv"
	"strings"
)

// NormalizeSurface rewrites text into the canonical surface form the WER
// comparison uses: **lowercase words, digits spelled out, contractions
// expanded, compounds split.**
//
// # Why
//
// `docs/52` measured 30 samples at 14.7%, and found that **10 of the 39 errors
// were pure formatting** — the recogniser heard the right words and wrote them
// differently. The tool reported all 39 as mistakes, so every future run would
// carry the same 3–4 point pessimism, and nothing would ever look wrong.
//
// # The admission rule
//
// A difference is normalised **only if it is another spelling of the same
// words** — i.e. it cannot be attributed to the recogniser mishearing. That is
// the whole test, and it is why the tables are small and explicit:
//
//   - `3` and `three` are one word spelled two ways. Admitted.
//   - `five` and `three` are two different words. **Not** admitted — see
//     `TestScoreWERNormalizedStillCountsRealSubstitutions`.
//
// Anything not in a table is left exactly as it was. That is deliberate: a
// cleverer rule (stemming, synonyms, fuzzy matching) makes the number harder to
// explain and to reproduce, and **this number exists to be argued with**.
//
// # Both numbers are reported
//
// This does not replace the raw scorer. `ScoreWER` is unchanged, and the tool
// prints raw and normalised side by side — see `cmd/asr-wer`. Replacing the
// pessimistic number with an optimistic one would just swap one unexplained
// figure for another; reporting both is what makes the gap itself legible, and
// the gap is the measurement of how much of the error is formatting.
func NormalizeSurface(text string) string {
	expanded := expandContractions(text)

	tokens := TokenizeWER(expanded)
	out := make([]string, 0, len(tokens))
	for _, tok := range tokens {
		if words, ok := numberWords(tok); ok {
			out = append(out, words...)
			continue
		}
		if parts, ok := compoundParts[tok]; ok {
			// Compounds are split rather than joined: splitting is total (any
			// spacing variant of the reference already reads as separate
			// tokens), whereas joining would have to guess where the seam is.
			out = append(out, parts...)
			continue
		}
		out = append(out, tok)
	}
	return strings.Join(out, " ")
}

// surfaceWord matches a run of letters, digits and apostrophes — one "word" as
// the text was written. Contraction expansion has to run **before**
// `TokenizeWER`, because that tokenizer drops apostrophes (so `it's` and `its`
// become indistinguishable, and expanding the wrong one would be a real error
// silently repaired).
var surfaceWord = regexp.MustCompile(`[A-Za-z0-9']+`)

func expandContractions(text string) string {
	return surfaceWord.ReplaceAllStringFunc(text, func(word string) string {
		if expansion, ok := contractionExpansions[strings.ToLower(word)]; ok {
			return expansion
		}
		return word
	})
}

// contractionExpansions: the contraction, spelled out.
//
// Keys keep their apostrophe so the possessive forms are never touched —
// `its`, `whose`, `theirs` are not contractions and must not be expanded into
// something they are not.
var contractionExpansions = map[string]string{
	"i've": "i have", "i'm": "i am", "i'll": "i will", "i'd": "i would",
	"you've": "you have", "you're": "you are", "you'll": "you will", "you'd": "you would",
	"we've": "we have", "we're": "we are", "we'll": "we will", "we'd": "we would",
	"they've": "they have", "they're": "they are", "they'll": "they will",
	"he's": "he is", "she's": "she is", "it's": "it is",
	"that's": "that is", "there's": "there is", "what's": "what is",
	"who's": "who is", "where's": "where is", "how's": "how is",
	"let's": "let us",
	"don't": "do not", "doesn't": "does not", "didn't": "did not",
	"can't": "cannot", "won't": "will not",
	"isn't": "is not", "aren't": "are not",
	"wasn't": "was not", "weren't": "were not",
	"hasn't": "has not", "haven't": "have not", "hadn't": "had not",
	"couldn't": "could not", "wouldn't": "would not", "shouldn't": "should not",
}

// compoundParts: one written word that is another written form's two words.
//
// **Additive only, and deliberately short.** Every entry is a pair someone
// actually observed in the sample set — a speculative entry makes the number
// less explainable, which is the thing this whole change is trying to fix.
// Measured pairs so far: `cleanup` / `clean up`, `backend` / `back end`.
var compoundParts = map[string][]string{
	"cleanup": {"clean", "up"},
	"backend": {"back", "end"},
}

// numberWords spells an integer as words, so `3` and `three` compare equal.
//
// Digits only: a *number word* against a *different* number word is a
// mishearing, not a spelling, and must keep counting as one. The range stops at
// 100 — beyond that the string is left alone rather than guessed at, because
// four-digit numbers in these samples are years and quantities where a wrong
// reading would be worse than a pessimistic one.
func numberWords(token string) ([]string, bool) {
	n, err := strconv.Atoi(token)
	if err != nil || n < 0 || n > 100 {
		return nil, false
	}
	switch {
	case n < 20:
		return []string{ones[n]}, true
	case n%10 == 0:
		return []string{tens[n/10]}, true
	case n == 100:
		return []string{"hundred"}, true
	default:
		return []string{tens[n/10], ones[n%10]}, true
	}
}

var ones = [20]string{
	"zero", "one", "two", "three", "four", "five", "six", "seven", "eight",
	"nine", "ten", "eleven", "twelve", "thirteen", "fourteen", "fifteen",
	"sixteen", "seventeen", "eighteen", "nineteen",
}

var tens = [11]string{
	"", "", "twenty", "thirty", "forty", "fifty", "sixty", "seventy",
	"eighty", "ninety", "hundred",
}

// ScoreWERNormalized scores after NormalizeSurface on both sides.
//
// Both sides go through the same normalisation, so the direction of a
// difference never matters: `cleanup` vs `clean up` and `clean up` vs `cleanup`
// canonicalise identically.
func ScoreWERNormalized(reference, hypothesis string) WERResult {
	return ScoreWER(NormalizeSurface(reference), NormalizeSurface(hypothesis))
}
