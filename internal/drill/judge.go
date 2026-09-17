package drill

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"
)

// Completer is the B16-shaped LLM seam. Tests inject a mock; production can
// wrap Ark chat-completions without importing AIOrchestrator.
type Completer interface {
	Complete(ctx context.Context, prompt string) (string, error)
}

// LLMJudge scores whether userSaid is semantically equivalent to target.
type LLMJudge struct {
	LLM Completer
	// Timeout overrides JudgeTimeout. Zero means the default.
	Timeout time.Duration
}

// JudgePrompt is the D-3 semantic-equivalence template.
//
// The rubric is explicit because "semantically equivalent" alone is not
// executable: measured against 200 gold cases, the unstated standard failed 40
// of them, every one because a plainly-correct answer was shorter than the
// target rather than different from it (86_ F2). The product's promise is recall,
// not recitation — so the gist passing is the rule, and completeness is
// reported rather than enforced.
func JudgePrompt(target, userSaid string) string {
	return fmt.Sprintf(`You are a spoken-English drill judge. The learner was shown a target phrase and said it back from memory.

Target: %q
Learner said: %q

Decide whether the learner's answer carries the same core proposition as the target.

PASS when the answer is understandable English and conveys the same core meaning, even if it is
shorter, leaves out optional detail (times, places, extra clauses, examples), or words it differently.
FAIL when the core meaning is missing or changed (a different action, a different object, the opposite
meaning, or an unrelated sentence), or the answer is not understandable English.

Do not require the learner to reproduce every detail of the target: a learner who recalls the point has
recalled the phrase.

Reply with JSON only:
{"pass": true|false, "judge_reason": "short reason", "omitted_details": true|false}
Set omitted_details true when the answer passed but dropped detail the target carried.`, target, userSaid)
}

// Judge calls the LLM under the judge's latency budget.
//
// A failure to judge is reported as Judged=false rather than as a failed answer:
// the sentinel reasons below are outcomes of *our* side (a deadline, a parse),
// and turning them into "the learner was wrong" is how a correct answer gets
// punished (86_ F1).
func (j *LLMJudge) Judge(ctx context.Context, target, userSaid string) (JudgeResult, error) {
	if j == nil || j.LLM == nil {
		return JudgeResult{Pass: false, Reason: "judge_unavailable", Judged: false}, nil
	}
	timeout := j.timeout()
	ctx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()
	raw, err := j.LLM.Complete(ctx, JudgePrompt(target, userSaid))
	if err != nil {
		incJudgeError()
		return JudgeResult{Pass: false, Reason: "judge_timeout", Judged: false}, nil
	}
	parsed, ok := parseJudgeJSON(raw)
	if !ok {
		incParseError()
		return JudgeResult{Pass: false, Reason: "judge_parse_error", Judged: false}, nil
	}
	parsed.Judged = true
	return parsed, nil
}

// Budget is the effective per-call budget. Exported so a caller can tell
// "the model was slow" apart from "we gave it no time".
func (j *LLMJudge) Budget() time.Duration { return j.timeout() }

// timeout returns the configured budget, or the measured default.
func (j *LLMJudge) timeout() time.Duration {
	if j != nil && j.Timeout > 0 {
		return j.Timeout
	}
	return JudgeTimeout
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
