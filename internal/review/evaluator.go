package review

import (
	"context"
	"encoding/json"
	"strings"
	"unicode/utf8"

	"github.com/FluentWork/fluentwork-backend/internal/corpus"
	"github.com/FluentWork/fluentwork-backend/internal/session"
)

// Evaluator scores one utterance with a 3s LLM budget.
type Evaluator struct {
	LLM Completer
}

// FallbackEval is used on timeout or parse failure.
func FallbackEval() UtteranceEval {
	return UtteranceEval{
		Score:       0.5,
		Dims:        session.EvalDims{Grammar: 0.5, Fluency: 0.5, Vocabulary: 0.5},
		Suggestions: []string{"系统繁忙，请稍后重试"},
		Reason:      "fallback",
	}
}

// ZeroEval is used for empty / unintelligible ASR (T-B18-6).
func ZeroEval() UtteranceEval {
	return UtteranceEval{
		Score:       0,
		Dims:        session.EvalDims{},
		Suggestions: []string{"无法识别，请再说一次"},
		Reason:      "abnormal_asr",
	}
}

// Evaluate scores one utterance. Failures return a fallback, not an error.
func (e *Evaluator) Evaluate(ctx context.Context, utt session.Utterance, hits []corpus.RecentHit) UtteranceEval {
	if isAbnormalASR(utt) {
		return ZeroEval()
	}
	if e == nil || e.LLM == nil {
		out := FallbackEval()
		out.Reason = "judge_unavailable"
		return out
	}
	ctx, cancel := context.WithTimeout(ctx, EvaluateTimeout)
	defer cancel()
	raw, err := e.LLM.Complete(ctx, EvalPrompt(utt, hits))
	if err != nil {
		incTimeout()
		out := FallbackEval()
		out.Reason = "timeout"
		return out
	}
	parsed, ok := parseEvalJSON(raw)
	if !ok {
		incParseError()
		out := FallbackEval()
		out.Reason = "parse_error"
		return out
	}
	return parsed
}

func isAbnormalASR(utt session.Utterance) bool {
	if strings.TrimSpace(utt.Text) == "" {
		return true
	}
	if utt.ASRConfidence != nil && *utt.ASRConfidence < LowASRThreshold {
		return true
	}
	return false
}

func parseEvalJSON(raw string) (UtteranceEval, bool) {
	trimmed := strings.TrimSpace(raw)
	if i := strings.Index(trimmed, "{"); i >= 0 {
		if j := strings.LastIndex(trimmed, "}"); j > i {
			trimmed = trimmed[i : j+1]
		}
	}
	var wire struct {
		Score       float64          `json:"score"`
		Dims        session.EvalDims `json:"dims"`
		Suggestions []string         `json:"suggestions"`
	}
	if err := json.Unmarshal([]byte(trimmed), &wire); err != nil {
		return UtteranceEval{}, false
	}
	out := UtteranceEval{
		Score:       clamp01(wire.Score),
		Dims:        session.EvalDims{Grammar: clamp01(wire.Dims.Grammar), Fluency: clamp01(wire.Dims.Fluency), Vocabulary: clamp01(wire.Dims.Vocabulary)},
		Suggestions: clampSuggestions(wire.Suggestions),
	}
	return out, true
}

func clamp01(v float64) float64 {
	if v < 0 {
		return 0
	}
	if v > 1 {
		return 1
	}
	return v
}

func clampSuggestions(in []string) []string {
	out := make([]string, 0, 3)
	for _, s := range in {
		s = strings.TrimSpace(s)
		if s == "" {
			continue
		}
		if utf8.RuneCountInString(s) > MaxSuggestionRunes {
			runes := []rune(s)
			s = string(runes[:MaxSuggestionRunes])
		}
		out = append(out, s)
		if len(out) == 3 {
			break
		}
	}
	return out
}

// StaticCompleter returns a fixed body for tests.
type StaticCompleter struct {
	Body string
	Err  error
}

// Complete implements Completer.
func (s StaticCompleter) Complete(context.Context, string) (string, error) {
	return s.Body, s.Err
}
