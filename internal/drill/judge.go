package drill

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
)

// Completer is the B16-shaped LLM seam. Tests inject a mock; production can
// wrap Ark chat-completions without importing AIOrchestrator.
type Completer interface {
	Complete(ctx context.Context, prompt string) (string, error)
}

// LLMJudge scores whether userSaid is semantically equivalent to target.
type LLMJudge struct {
	LLM Completer
}

// JudgePrompt is the D-3 semantic-equivalence template.
func JudgePrompt(target, userSaid string) string {
	return fmt.Sprintf(`You are a spoken-English drill judge. Decide if the learner's utterance is semantically equivalent to the target phrase.

Target: %q
Learner said: %q

Reply with JSON only: {"pass": true|false, "judge_reason": "short reason"}`, target, userSaid)
}

// Judge calls the LLM with a 1.5s timeout. Parse failures are a miss.
func (j *LLMJudge) Judge(ctx context.Context, target, userSaid string) (JudgeResult, error) {
	if j == nil || j.LLM == nil {
		return JudgeResult{Pass: false, Reason: "judge_unavailable"}, nil
	}
	ctx, cancel := context.WithTimeout(ctx, JudgeTimeout)
	defer cancel()
	raw, err := j.LLM.Complete(ctx, JudgePrompt(target, userSaid))
	if err != nil {
		incJudgeError()
		return JudgeResult{Pass: false, Reason: "judge_timeout"}, nil
	}
	parsed, ok := parseJudgeJSON(raw)
	if !ok {
		incParseError()
		return JudgeResult{Pass: false, Reason: "judge_parse_error"}, nil
	}
	return parsed, nil
}

func parseJudgeJSON(raw string) (JudgeResult, bool) {
	trimmed := strings.TrimSpace(raw)
	if i := strings.Index(trimmed, "{"); i >= 0 {
		if j := strings.LastIndex(trimmed, "}"); j > i {
			trimmed = trimmed[i : j+1]
		}
	}
	var out JudgeResult
	if err := json.Unmarshal([]byte(trimmed), &out); err != nil {
		return JudgeResult{}, false
	}
	if strings.TrimSpace(out.Reason) == "" {
		if out.Pass {
			out.Reason = "semantic_match"
		} else {
			out.Reason = "semantic_mismatch"
		}
	}
	return out, true
}

// StaticCompleter returns a fixed completion; used in tests.
type StaticCompleter struct {
	Body string
	Err  error
}

// Complete implements Completer.
func (s StaticCompleter) Complete(context.Context, string) (string, error) {
	return s.Body, s.Err
}
