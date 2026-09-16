package materials

import (
	"context"

	"github.com/FluentWork/fluentwork-backend/internal/orchestrator"
)

// OrchestratorAdapter bridges orchestrator.Client to materials.Completer.
type OrchestratorAdapter struct {
	Client orchestrator.Client
}

func (a *OrchestratorAdapter) Complete(ctx context.Context, prompt string) (string, error) {
	resp, err := a.Client.Complete(ctx, orchestrator.CompletionRequest{
		Prompt:      prompt,
		MaxTokens:   500,
		Temperature: 0,
		Operation:   "materials.refine",
	})
	if err != nil {
		return "", err
	}
	return resp.Content, nil
}
