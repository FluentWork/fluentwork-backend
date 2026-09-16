package review

import (
	"context"

	"github.com/FluentWork/fluentwork-backend/internal/orchestrator"
)

// OrchestratorAdapter adapts orchestrator.Client to review.Completer interface.
type OrchestratorAdapter struct {
	Client orchestrator.Client
}

// Complete implements Completer by calling orchestrator.Client.
func (a *OrchestratorAdapter) Complete(ctx context.Context, prompt string) (string, error) {
	resp, err := a.Client.Complete(ctx, orchestrator.CompletionRequest{
		Prompt:      prompt,
		MaxTokens:   500,
		Temperature: 0,
		Operation:   "review.eval",
	})
	if err != nil {
		return "", err
	}
	return resp.Content, nil
}
