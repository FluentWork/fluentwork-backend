package voicepoc

import (
	"strings"
	"unicode"
)

// WERResult is one utterance scored against its ground truth.
//
// The error counts are kept apart rather than collapsed into a single distance:
// they fail differently and they mean different things for this product.
// Substitutions look like "the model heard a different word" (accent), deletions
// look like "the model heard nothing" (dropped endings, swallowed syllables),
// and insertions look like "the model invented a word" (epenthetic vowels in
// consonant clusters). A pipeline decision that only knows the total cannot tell
// those apart.
type WERResult struct {
	Substitutions int
	Deletions     int
	Insertions    int
	Correct       int
	// ReferenceLength is N in WER = (S + D + I) / N.
	ReferenceLength int
}

// Errors is the numerator of WER.
func (r WERResult) Errors() int { return r.Substitutions + r.Deletions + r.Insertions }

// Rate is WER. An empty reference has no rate — see ScoreWER.
func (r WERResult) Rate() float64 {
	if r.ReferenceLength == 0 {
		return 0
	}
	return float64(r.Errors()) / float64(r.ReferenceLength)
}

// TokenizeWER lowercases, drops punctuation **and apostrophes**, and splits on
// whitespace.
//
// Apostrophes are dropped on purpose: ASR output does not punctuate, the
// references are written for humans and do, so keeping them scores `let's`
// against `lets` as a substitution — a correct transcription penalised for a
// character the recogniser had no reason to emit. The cost is that `its` and
// `it's` collapse; for scoring speech that is the right trade.
//
// Otherwise deliberately crude. Anything cleverer (stemming, number
// normalisation) makes the number harder to explain and to reproduce, and this
// measurement exists to be argued with by whoever reads it.
//
// CJK runs are kept whole as single tokens: the ASR returns Chinese for Chinese
// speech, and splitting 中文 into per-character tokens would make a Chinese
// answer score as a near-total miss against an English reference for reasons
// that have nothing to do with accent.
func TokenizeWER(text string) []string {
	var (
		tokens  []string
		current strings.Builder
	)
	flush := func() {
		if current.Len() > 0 {
			tokens = append(tokens, strings.ToLower(current.String()))
			current.Reset()
		}
	}
	for _, r := range text {
		switch {
		case unicode.IsLetter(r) || unicode.IsDigit(r):
			current.WriteRune(r)
		case r == '\'' || r == '\u2019':
			// Skipped, **not** a separator: `let's` must become the single token
			// `lets`, not `let` + `s`. Splitting there would score one correct
			// word as one substitution plus one deletion.
		default:
			flush()
		}
	}
	flush()
	return tokens
}

// ScoreWER aligns hypothesis against reference and counts each error kind.
//
// Standard Levenshtein alignment with a backtrace, so the three counts come from
// the same path rather than from three separate heuristics.
func ScoreWER(reference, hypothesis string) WERResult {
	ref := TokenizeWER(reference)
	hyp := TokenizeWER(hypothesis)

	result := WERResult{ReferenceLength: len(ref)}
	if len(ref) == 0 {
		// No reference to be wrong about. Counting the hypothesis as insertions
		// would report a rate against zero, which is not a number — the caller
		// decides what an empty reference means.
		return result
	}
	if len(hyp) == 0 {
		result.Deletions = len(ref)
		return result
	}

	// cost[i][j] = edit distance between ref[:i] and hyp[:j]
	cost := make([][]int, len(ref)+1)
	for i := range cost {
		cost[i] = make([]int, len(hyp)+1)
		cost[i][0] = i
	}
	for j := 0; j <= len(hyp); j++ {
		cost[0][j] = j
	}
	for i := 1; i <= len(ref); i++ {
		for j := 1; j <= len(hyp); j++ {
			if ref[i-1] == hyp[j-1] {
				cost[i][j] = cost[i-1][j-1]
				continue
			}
			sub := cost[i-1][j-1] + 1
			del := cost[i-1][j] + 1
			ins := cost[i][j-1] + 1
			cost[i][j] = min(sub, del, ins)
		}
	}

	// Walk back to attribute each step. Ties resolve substitution-first, then
	// deletion, then insertion — a fixed order so the same pair always produces
	// the same counts, which is what makes the number reproducible.
	for i, j := len(ref), len(hyp); i > 0 || j > 0; {
		switch {
		case i > 0 && j > 0 && ref[i-1] == hyp[j-1] && cost[i][j] == cost[i-1][j-1]:
			result.Correct++
			i--
			j--
		case i > 0 && j > 0 && cost[i][j] == cost[i-1][j-1]+1:
			result.Substitutions++
			i--
			j--
		case i > 0 && cost[i][j] == cost[i-1][j]+1:
			result.Deletions++
			i--
		default:
			result.Insertions++
			j--
		}
	}
	return result
}
