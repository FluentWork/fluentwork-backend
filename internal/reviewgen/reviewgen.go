package reviewgen

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/eval"
	"github.com/FluentWork/fluentwork-backend/pkg/logx"
)

// Generator builds review/refine artifacts from a finished transcript.
type Generator interface {
	Generate(ctx context.Context, req Request) (Result, error)
}

// Request is the normalized input sent to one review/refine generation call.
type Request struct {
	SessionID  string
	UserID     string
	SceneType  string
	Transcript string
}

// Result is one successful generation output.
type Result struct {
	Review    json.RawMessage
	Refine    json.RawMessage
	Generator string
	Model     string
	TokensIn  int
	TokensOut int
}

// ArkGenerator calls the Ark chat-completions endpoint for review/refine generation.
type ArkGenerator struct {
	BaseURL    string
	APIKey     string
	Endpoint   string
	HTTPClient *http.Client
	Logger     *slog.Logger
}

// Enabled reports whether the generator has enough config to make live calls.
func (g ArkGenerator) Enabled() bool {
	return strings.TrimSpace(g.BaseURL) != "" && strings.TrimSpace(g.APIKey) != "" && strings.TrimSpace(g.Endpoint) != ""
}

// Generate calls Ark once and validates both review and refine artifacts against B15 rules.
func (g ArkGenerator) Generate(ctx context.Context, req Request) (Result, error) {
	if !g.Enabled() {
		return Result{}, fmt.Errorf("ark review generator is not configured")
	}
	req.SessionID = strings.TrimSpace(req.SessionID)
	req.SceneType = strings.TrimSpace(req.SceneType)
	req.Transcript = strings.TrimSpace(req.Transcript)
	if req.SessionID == "" {
		return Result{}, fmt.Errorf("session_id is required")
	}
	if req.SceneType == "" {
		return Result{}, fmt.Errorf("scene_type is required")
	}
	if req.Transcript == "" {
		return Result{}, fmt.Errorf("transcript is required")
	}

	seg := logx.Begin(g.Logger, "review.generate",
		"provider", "ark",
		"model", g.Endpoint,
		"session_id", req.SessionID,
		"stage", "orchestration",
	)
	var generateErr error
	var endAttrs []any
	defer func() {
		seg.End(generateErr, endAttrs...)
	}()

	payload := arkChatRequest{
		Model: g.Endpoint,
		Messages: []arkMessage{
			{Role: "system", Content: systemPrompt()},
			{Role: "user", Content: userPrompt(req)},
		},
		MaxTokens:   800,
		Temperature: 0,
		ResponseFormat: map[string]any{
			"type": "json_object",
		},
		// Review endpoint is bound to a thinking-capable model; leaving thinking
		// enabled causes multi-minute / timeout hangs under json_object workloads.
		Thinking: map[string]any{"type": "disabled"},
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		generateErr = err
		return Result{}, generateErr
	}

	httpClient := g.HTTPClient
	if httpClient == nil {
		httpClient = &http.Client{
			Timeout: 90 * time.Second,
			Transport: &http.Transport{
				Proxy:               http.ProxyFromEnvironment,
				ForceAttemptHTTP2:   false,
				DialContext:         (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
				TLSHandshakeTimeout: 15 * time.Second,
			},
		}
	}
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(g.BaseURL, "/")+"/chat/completions", bytes.NewReader(raw))
	if err != nil {
		generateErr = err
		return Result{}, generateErr
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+strings.TrimSpace(g.APIKey))

	resp, err := httpClient.Do(httpReq)
	if err != nil {
		generateErr = err
		return Result{}, generateErr
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 2<<20))
	if err != nil {
		generateErr = err
		return Result{}, generateErr
	}
	if resp.StatusCode != http.StatusOK {
		generateErr = fmt.Errorf("ark chat completions http=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
		return Result{}, generateErr
	}

	var decoded arkChatResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		generateErr = fmt.Errorf("decode ark response: %w", err)
		return Result{}, generateErr
	}
	content := strings.TrimSpace(firstChoiceContent(decoded))
	if content == "" {
		generateErr = fmt.Errorf("ark response missing message content")
		return Result{}, generateErr
	}

	doc, err := parseGeneratedDocument(content)
	if err != nil {
		generateErr = err
		return Result{}, generateErr
	}
	findings := eval.ValidateSample(eval.Sample{
		ID:         req.SessionID,
		Transcript: req.Transcript,
		Review:     doc.Review,
		Refine:     doc.Refine,
	})
	if len(findings) > 0 {
		generateErr = fmt.Errorf("generated document failed B15 validation: %s", findings[0].Rule)
		return Result{}, generateErr
	}

	result := Result{
		Review:    doc.Review,
		Refine:    doc.Refine,
		Generator: "ark-review-refine-v1",
		Model:     g.Endpoint,
	}
	if decoded.Usage != nil {
		result.TokensIn = decoded.Usage.PromptTokens
		result.TokensOut = decoded.Usage.CompletionTokens
		endAttrs = []any{
			"tokens_in", result.TokensIn,
			"tokens_out", result.TokensOut,
		}
	}
	return result, nil
}

type generatedDocument struct {
	Review json.RawMessage `json:"review"`
	Refine json.RawMessage `json:"refine"`
}

type arkChatRequest struct {
	Model          string         `json:"model"`
	Messages       []arkMessage   `json:"messages"`
	MaxTokens      int            `json:"max_tokens"`
	Temperature    float64        `json:"temperature"`
	ResponseFormat map[string]any `json:"response_format,omitempty"`
	Thinking       map[string]any `json:"thinking,omitempty"`
}

type arkMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type arkChatResponse struct {
	Choices []struct {
		Message arkMessage `json:"message"`
	} `json:"choices"`
	Usage *struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
	} `json:"usage"`
}

func parseGeneratedDocument(raw string) (generatedDocument, error) {
	trimmed := strings.TrimSpace(raw)
	trimmed = strings.TrimPrefix(trimmed, "```json")
	trimmed = strings.TrimPrefix(trimmed, "```")
	trimmed = strings.TrimSuffix(trimmed, "```")
	trimmed = strings.TrimSpace(trimmed)

	var doc generatedDocument
	if err := json.Unmarshal([]byte(trimmed), &doc); err != nil {
		return generatedDocument{}, fmt.Errorf("decode generated json: %w", err)
	}
	if len(doc.Review) == 0 || len(doc.Refine) == 0 {
		return generatedDocument{}, fmt.Errorf("generated json must include review and refine")
	}
	return doc, nil
}

func firstChoiceContent(resp arkChatResponse) string {
	if len(resp.Choices) == 0 {
		return ""
	}
	return resp.Choices[0].Message.Content
}

func systemPrompt() string {
	return strings.TrimSpace(`
You are FluentWork review-refine generator.
Return one JSON object only with exactly two top-level keys: review and refine.
Do not add any other keys.

Required output shape:
{
  "review": {
    "goal_achievement": {"met": boolean, "note": string},
    "issues": [{"type":"grammar|idiomatic|missing_info","original_quote": string, "hint": string}],
    "suggestions": [{"text": string}],
    "comparisons": [object, object, object]
  },
  "refine": {
    "blocks": [{
      "intent_zh": string,
      "expression_en": string,
      "anchor_user_said": string,
      "scene_tag": string,
      "function_tag": string
    }]
  }
}
Rules:
- output valid JSON only
- review must be an object, never a string
- refine must be an object, never a string
- review must contain exactly: goal_achievement, issues, suggestions, comparisons
- refine must contain exactly: blocks
- issues <= 5
- suggestions <= 3
- comparisons length 3-8
- every original_quote and anchor_user_said must be exact substrings from the transcript
- scene_tag must equal the provided scene type
- function_tag must be one of: object, clarify, report, propose, agree, disagree, ask, summarize, defer, commit
- keep every string concise
- when there is no issue, use [] rather than prose
- comparisons items must use keys user and better only

Example:
{
  "review": {
    "goal_achievement": {"met": true, "note": "Clear blocker and next step."},
    "issues": [{"type": "idiomatic", "original_quote": "sync up with the team", "hint": "Prefer touch base with the team."}],
    "suggestions": [{"text": "Use touch base with the team."}],
    "comparisons": [
      {"user": "sync up with the team", "better": "touch base with the team"},
      {"user": "I am blocked on the API review", "better": "I'm blocked waiting on the API review"},
      {"user": "tomorrow", "better": "tomorrow morning"}
    ]
  },
  "refine": {
    "blocks": [
      {"intent_zh": "同步进度", "expression_en": "I'll touch base with the team tomorrow.", "anchor_user_said": "sync up with the team", "scene_tag": "standup", "function_tag": "report"}
    ]
  }
}
`)
}

func userPrompt(req Request) string {
	return fmt.Sprintf("scene_type: %s\nsession_id: %s\ntranscript:\n%s", req.SceneType, req.SessionID, req.Transcript)
}
