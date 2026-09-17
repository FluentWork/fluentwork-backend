package topic

import (
	"context"

	"github.com/FluentWork/fluentwork-backend/internal/orchestrator"
)

// OrchestratorAdapter bridges orchestrator.Client to topic.Completer.
type OrchestratorAdapter struct {
	Client orchestrator.Client
}

// Complete implements the generator's LLM client by routing one prompt through
// orchestrator.Client (operation topic.generate).
func (a *OrchestratorAdapter) Complete(ctx context.Context, prompt string) (string, error) {
	resp, err := a.Client.Complete(ctx, orchestrator.CompletionRequest{
		Prompt:      prompt,
		MaxTokens:   800,
		Temperature: 0.7,
		Operation:   "topic.generate",
	})
	if err != nil {
		return "", err
	}
	return resp.Content, nil
}
