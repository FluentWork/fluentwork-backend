// Package review implements B18 per-utterance review eval (grammar / fluency / vocabulary).
package review

import (
	"context"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/session"
)

const (
	// EvaluateTimeout is the D-1 budget for one utterance eval call.
	EvaluateTimeout = 3 * time.Second
	// DefaultQPSInterval is the gap between utterance eval calls.
	DefaultQPSInterval = time.Second
	// MaxSuggestionRunes caps each suggestion (T-B18-5).
	MaxSuggestionRunes = 30
	// LowASRThreshold treats very uncertain ASR as score 0.
	LowASRThreshold = 0.3
)

// Completer is the B16-shaped LLM seam.
type Completer interface {
	Complete(ctx context.Context, prompt string) (string, error)
}

// UtteranceEval is stored in utterances.llm_eval_json.
type UtteranceEval struct {
	Score       float64          `json:"score"`
	Dims        session.EvalDims `json:"dims"`
	Suggestions []string         `json:"suggestions"`
	Reason      string           `json:"reason,omitempty"`
}
