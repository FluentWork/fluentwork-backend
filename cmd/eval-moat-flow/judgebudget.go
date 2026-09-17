package main

import (
	"context"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/drill"
	"github.com/FluentWork/fluentwork-backend/internal/orchestrator"
)

// The drill judge runs inside a latency budget in production. That budget is a
// latency contract, not a statement about judgement quality — and an eval must
// not confuse the two: a timed-out judge returns "fail", which would silently
// score as a verdict.
//
// So each case is judged twice, through the same production code path:
//
//   - under the production budget, to measure how often the realtime path can
//     even get an answer (a health signal about the budget);
//   - with a generous budget, to measure whether the prompt judges correctly
//     when it is allowed to finish (that is the accuracy number).
//
// Both passes use drill.LLMJudge — same prompt, same parsing, same endpoint —
// so the only variable between them is time.
type judgeBudget struct {
	slow *drill.LLMJudge
}

// defaultAccuracyTimeout is far above the production budget on purpose: this
// pass asks "is the judgement right", not "is it fast".
const defaultAccuracyTimeout = 45 * time.Second

// judgeUnderProductionBudget runs the production judge path unchanged.
func judgeUnderProductionBudget(ctx context.Context, j *drill.LLMJudge, target, answer string) (drill.JudgeResult, time.Duration) {
	started := time.Now()
	result, _ := j.Judge(ctx, target, answer)
	return result, time.Since(started)
}

// judgeWithRoom re-runs the same judge with a budget that lets it finish.
func (b *judgeBudget) judgeWithRoom(ctx context.Context, target, answer string) (drill.JudgeResult, time.Duration, error) {
	started := time.Now()
	result, err := b.slow.Judge(ctx, target, answer)
	return result, time.Since(started), err
}

func newJudgeBudget(client orchestrator.Client) *judgeBudget {
	return &judgeBudget{slow: &drill.LLMJudge{
		LLM:     &drill.OrchestratorAdapter{Client: client},
		Timeout: defaultAccuracyTimeout,
	}}
}
