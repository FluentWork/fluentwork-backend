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

// Enabled reports whether the adapter is ready for use.
func (a *OrchestratorAdapter) Enabled() bool {
	return a.Client != nil
}

// Generate calls orchestrator.Client and validates the review/refine output.
func (a *OrchestratorAdapter) Generate(ctx context.Context, req Request) (Result, error) {
	req.SessionID = strings.TrimSpace(req.SessionID)
	req.SceneType = strings.TrimSpace(req.SceneType)
	req.Transcript = strings.TrimSpace(req.Transcript)
	if req.SessionID == "" {
		return Result{}, &GenerateError{Kind: FailureInvalidRequest, Err: fmt.Errorf("session_id is required")}
	}
	if req.SceneType == "" {
		return Result{}, &GenerateError{Kind: FailureInvalidRequest, Err: fmt.Errorf("scene_type is required")}
	}
	if _, ok := eval.SceneTags[req.SceneType]; !ok {
		req.SceneType = "standup"
	}
	if req.Transcript == "" {
		return Result{}, &GenerateError{
			Kind:      FailureEmptySession,
			Err:       fmt.Errorf("transcript is required"),
			SessionID: req.SessionID,
		}
	}

	resp, err := a.Client.Complete(ctx, orchestrator.CompletionRequest{
		// The user travels with the call so the cost row is attributed by the
		// generic writer, instead of the session writing a second row of its own.
		UserID:         req.UserID,
		SystemPrompt:   systemPrompt(),
		Prompt:         userPrompt(req),
		MaxTokens:      800,
		Temperature:    0,
		ResponseFormat: "json_object",
		Operation:      "reviewgen.generate",
	})
	if err != nil {
		// The transport failed before any content existed; there is no raw
		// model output to attach, only the provider's own error.
		return Result{}, &GenerateError{
			Kind:      FailureTransport,
			Err:       err,
			SessionID: req.SessionID,
		}
	}

	content := strings.TrimSpace(resp.Content)
	if content == "" {
		return Result{}, &GenerateError{
			Kind:         FailureEmptyContent,
			Err:          fmt.Errorf("orchestrator response missing content"),
			SessionID:    req.SessionID,
			FinishReason: resp.FinishReason,
		}
	}

	doc, err := parseGeneratedDocument(content, resp.FinishReason)
	if err != nil {
		return Result{}, withSession(err, req.SessionID)
	}
	findings := eval.ValidateSample(eval.Sample{
		ID:         req.SessionID,
		Transcript: req.Transcript,
		Review:     doc.Review,
		Refine:     doc.Refine,
	})
	if len(findings) > 0 {
		return Result{}, schemaViolation(req.SessionID, content, resp.FinishReason, findings)
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
