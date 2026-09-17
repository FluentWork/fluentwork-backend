package main

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/drill"
	"github.com/FluentWork/fluentwork-backend/internal/orchestrator"
)

// The drill judge runs inside a 1.5s budget in production (drill.JudgeTimeout,
// the D-3 realtime budget). That budget is a latency contract, not a statement
// about judgement quality — and an eval must not confuse the two: a timed-out
// judge returns "fail", which would silently score as a verdict.
//
// So each case is judged twice:
//
//   - under the production budget, to measure how often the realtime path can
//     even get an answer (prod_timed_out → a health signal);
//   - with a generous budget, to measure whether the prompt judges correctly
//     when it is allowed to finish (that is the accuracy number).
type judgeBudget struct {
	client orchestrator.Client
	// accuracyTimeout is far above the production budget on purpose: this pass
	// asks "is the judgement right", not "is it fast".
	accuracyTimeout time.Duration
}

const defaultAccuracyTimeout = 30 * time.Second

// verdict is one judge answer plus how it was obtained.
type verdict struct {
	Pass     bool
	Reason   string
	TimedOut bool
	Duration time.Duration
}

// judgeUnderProductionBudget runs the production judge path unchanged.
func judgeUnderProductionBudget(ctx context.Context, j *drill.LLMJudge, target, answer string) verdict {
	started := time.Now()
	result, err := j.Judge(ctx, target, answer)
	elapsed := time.Since(started)
	v := verdict{Pass: result.Pass, Reason: result.Reason, Duration: elapsed}
	// drill.JudgeTimeout is exported, so the sentinel can be read rather than
	// guessed from the reason string.
	if err != nil || elapsed >= drill.JudgeTimeout || result.Reason == "judge_timeout" {
		v.TimedOut = true
	}
	return v
}

// judgeWithBudget asks the same prompt with room to finish.
func (b *judgeBudget) judgeWithBudget(ctx context.Context, target, answer string) (verdict, error) {
	started := time.Now()
	timeout := b.accuracyTimeout
	if timeout <= 0 {
		timeout = defaultAccuracyTimeout
	}
	callCtx, cancel := context.WithTimeout(ctx, timeout)
	defer cancel()

	prompt := drill.JudgePrompt(target, answer)
	resp, err := b.client.Complete(callCtx, orchestrator.CompletionRequest{
		Prompt:         prompt,
		MaxTokens:      200,
		Temperature:    0,
		ResponseFormat: "json_object",
		Operation:      "eval.moat.judge",
	})
	if err != nil {
		return verdict{}, err
	}
	var parsed struct {
		Pass   bool   `json:"pass"`
		Reason string `json:"judge_reason"`
	}
	content := strings.TrimSpace(resp.Content)
	if i := strings.Index(content, "{"); i >= 0 {
		content = content[i:]
	}
	if j := strings.LastIndex(content, "}"); j >= 0 {
		content = content[:j+1]
	}
	if err := json.Unmarshal([]byte(content), &parsed); err != nil {
		return verdict{}, err
	}
	return verdict{Pass: parsed.Pass, Reason: parsed.Reason, Duration: time.Since(started)}, nil
}
