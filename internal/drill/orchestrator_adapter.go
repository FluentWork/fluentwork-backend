package drill

import (
	"context"

	"github.com/FluentWork/fluentwork-backend/internal/orchestrator"
)

// OrchestratorAdapter bridges orchestrator.Client to drill.Completer.
type OrchestratorAdapter struct {
	Client orchestrator.Client
}

// Complete implements LLMJudge's client for the drill recall judge.
func (a *OrchestratorAdapter) Complete(ctx context.Context, prompt string) (string, error) {
	resp, err := a.Client.Complete(ctx, orchestrator.CompletionRequest{
		Prompt:         prompt,
		MaxTokens:      200,
		Temperature:    0,
		ResponseFormat: "json_object",
		Operation:      "drill.judge",
	})
	if err != nil {
		return "", err
	}
	return resp.Content, nil
}
