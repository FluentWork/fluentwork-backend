package reviewgen

import (
	"context"
	"fmt"
	"strings"

	"github.com/FluentWork/fluentwork-backend/internal/eval"
	"github.com/FluentWork/fluentwork-backend/internal/orchestrator"
)

// OrchestratorAdapter wraps orchestrator.Client to implement Generator.
type OrchestratorAdapter struct {
	Client orchestrator.Client
}

// Generate calls orchestrator.Client and validates the review/refine output.
func (a *OrchestratorAdapter) Generate(ctx context.Context, req Request) (Result, error) {
	req.SessionID = strings.TrimSpace(req.SessionID)
	req.SceneType = strings.TrimSpace(req.SceneType)
	req.Transcript = strings.TrimSpace(req.Transcript)
	if req.SessionID == "" {
		return Result{}, fmt.Errorf("session_id is required")
	}
	if req.SceneType == "" {
		return Result{}, fmt.Errorf("scene_type is required")
	}
	if _, ok := eval.SceneTags[req.SceneType]; !ok {
		req.SceneType = "standup"
	}
	if req.Transcript == "" {
		return Result{}, fmt.Errorf("transcript is required")
	}

	resp, err := a.Client.Complete(ctx, orchestrator.CompletionRequest{
		SystemPrompt:   systemPrompt(),
		Prompt:         userPrompt(req),
		MaxTokens:      800,
		Temperature:    0,
		ResponseFormat: "json_object",
		Operation:      "reviewgen.generate",
	})
	if err != nil {
		return Result{}, err
	}

	content := strings.TrimSpace(resp.Content)
	if content == "" {
		return Result{}, fmt.Errorf("orchestrator response missing content")
	}

	doc, err := parseGeneratedDocument(content)
	if err != nil {
		return Result{}, err
	}
	findings := eval.ValidateSample(eval.Sample{
		ID:         req.SessionID,
		Transcript: req.Transcript,
		Review:     doc.Review,
		Refine:     doc.Refine,
	})
	if len(findings) > 0 {
		return Result{}, fmt.Errorf("generated document failed B15 validation: %s", findings[0].Rule)
	}

	return Result{
		Review:    doc.Review,
		Refine:    doc.Refine,
		Generator: "orchestrator-review-refine-v1",
		Model:     resp.Model,
		TokensIn:  resp.PromptTokens,
		TokensOut: resp.OutputTokens,
	}, nil
}
