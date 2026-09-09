package review

import (
	"fmt"
	"strings"

	"github.com/FluentWork/fluentwork-backend/internal/corpus"
	"github.com/FluentWork/fluentwork-backend/internal/session"
)

// EvalPrompt is the D-1 three-dimension spoken-English eval template.
func EvalPrompt(utt session.Utterance, hits []corpus.RecentHit) string {
	var hitLines strings.Builder
	if len(hits) == 0 {
		hitLines.WriteString("(none)")
	} else {
		for _, hit := range hits {
			fmt.Fprintf(&hitLines, "- %s / %s\n", hit.IntentZH, hit.ChunkEN)
		}
	}
	return fmt.Sprintf(`You are a spoken-English coach. Score this learner utterance.

Utterance: %q
ASR confidence: %s
Recent phrase hits:
%s

Reply with JSON only:
{"score": 0.0-1.0, "dims": {"grammar": 0.0-1.0, "fluency": 0.0-1.0, "vocabulary": 0.0-1.0}, "suggestions": ["short tip", "short tip"]}
Each suggestion must be at most 30 characters. Use score 0 when the utterance is empty or unintelligible.`,
		utt.Text, confidenceLabel(utt), strings.TrimSpace(hitLines.String()))
}

func confidenceLabel(utt session.Utterance) string {
	if utt.ASRConfidence == nil {
		return "unknown"
	}
	return fmt.Sprintf("%.2f", *utt.ASRConfidence)
}
