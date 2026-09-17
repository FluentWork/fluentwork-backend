package conversation

import (
	"context"
	"strings"

	"github.com/FluentWork/fluentwork-backend/internal/orchestrator"
)

// OrchestratorLLM adapts the app-server's LLM seam to the rescue generator.
//
// Keeping the model call here rather than in the gateway means the ladder is
// billed like every other call (one ai_cost_logs row per rung, attributed to the
// learner) and the prompt stays with the rest of the product's prompts.
type OrchestratorLLM struct {
	Client orchestrator.Client
}

// Complete implements LLMClient.
func (o OrchestratorLLM) Complete(ctx context.Context, prompt string, options CompletionOptions) (string, error) {
	resp, err := o.Client.Complete(ctx, orchestrator.CompletionRequest{
		Prompt:      prompt,
		MaxTokens:   options.MaxTokens,
		Temperature: options.Temperature,
		Operation:   "rescue.ladder",
	})
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(resp.Content), nil
}
