// Package orchestrator is the app-server's single LLM entry point: every
// feature that needs a completion goes through Client, so model choice,
// cost logging and prompt-call bookkeeping live in one place.
package orchestrator

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"strings"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/config"
)

// ArkClient Ark LLM 客户端。模型按 operation 路由到各自的部署，
// 未配置路由的 operation 回落到 model（评论/炼化端点）。
type ArkClient struct {
	baseURL string
	apiKey  string
	// model is the fallback deployment for operations with no route.
	model string
	// endpoints routes an operation to its own deployment, so the per-task
	// endpoints configured in the console are actually used.
	endpoints  map[string]string
	costWriter CostWriter
	httpClient *http.Client
}

// NewArkClient 创建 Ark 客户端
func NewArkClient(cfg config.Config, costWriter CostWriter) *ArkClient {
	baseURL := strings.TrimSpace(cfg.ArkBaseURL)
	if baseURL == "" {
		baseURL = "https://ark.cn-beijing.volces.com/api/v3"
	}

	return &ArkClient{
		baseURL:    baseURL,
		apiKey:     cfg.ArkAPIKey,
		model:      strings.TrimSpace(cfg.ArkReviewRefineEP),
		endpoints:  endpointRouting(cfg),
		costWriter: costWriter,
		httpClient: &http.Client{
			Timeout: httpTimeout(cfg),
			Transport: &http.Transport{
				DialContext: (&net.Dialer{
					Timeout:   5 * time.Second,
					KeepAlive: 30 * time.Second,
				}).DialContext,
				MaxIdleConns:        100,
				IdleConnTimeout:     90 * time.Second,
				MaxIdleConnsPerHost: 20,
			},
		},
	}
}

// httpTimeout bounds one provider call; zero or unset means the shipping default.
func httpTimeout(cfg config.Config) time.Duration {
	if cfg.ArkHTTPTimeout > 0 {
		return cfg.ArkHTTPTimeout
	}
	return 30 * time.Second
}

// endpointRouting maps an operation to the deployment that should serve it.
//
// Every deployment used to run on the review endpoint, whatever its operation:
// the console had six endpoints configured per task and only one was ever
// called. Routing makes the intended split real — the flash-drill judge, for
// instance, is a short, latency-sensitive call that has no business running on
// the same (larger, slower, pricier) model as review generation.
//
// An operation with no mapping falls back to the review endpoint, so adding a
// caller never silently breaks it.
func endpointRouting(cfg config.Config) map[string]string {
	routes := map[string]string{}
	set := func(operation, endpoint string) {
		if endpoint = strings.TrimSpace(endpoint); endpoint != "" {
			routes[operation] = endpoint
		}
	}
	set("reviewgen.generate", cfg.ArkReviewRefineEP)
	set("review.refine", cfg.ArkReviewRefineEP)
	set("daily.read", cfg.ArkDailyReadEP)
	set("topic.generate", cfg.ArkTopicCardEP)
	set("hit.match", cfg.ArkHitMatchEP)
	set("drill.judge", cfg.ArkDrillJudgeEP)
	set("materials.refine", cfg.ArkTextDegradeEP)
	return routes
}

// endpointFor resolves the deployment for one operation.
func (a *ArkClient) endpointFor(operation string) string {
	if endpoint, ok := a.endpoints[strings.TrimSpace(operation)]; ok && endpoint != "" {
		return endpoint
	}
	return a.model
}

// Routing returns the operation→endpoint map this client will use, for startup
// logging: an operator should be able to see which model each task lands on.
func (a *ArkClient) Routing() map[string]string {
	out := make(map[string]string, len(a.endpoints))
	for operation, endpoint := range a.endpoints {
		out[operation] = endpoint
	}
	return out
}

type arkChatRequest struct {
	Model          string           `json:"model"`
	Messages       []arkChatMessage `json:"messages"`
	MaxTokens      int              `json:"max_tokens"`
	Temperature    float64          `json:"temperature"`
	ResponseFormat map[string]any   `json:"response_format,omitempty"`
	Thinking       map[string]any   `json:"thinking,omitempty"`
}

type arkChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type arkChatResponse struct {
	ID      string `json:"id"`
	Object  string `json:"object"`
	Created int64  `json:"created"`
	Model   string `json:"model"`
	Choices []struct {
		Index   int `json:"index"`
		Message struct {
			Role    string `json:"role"`
			Content string `json:"content"`
		} `json:"message"`
		FinishReason string `json:"finish_reason"`
	} `json:"choices"`
	Usage struct {
		PromptTokens     int `json:"prompt_tokens"`
		CompletionTokens int `json:"completion_tokens"`
		TotalTokens      int `json:"total_tokens"`
	} `json:"usage"`
}

// Complete 实现 Client 接口
func (a *ArkClient) Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
	start := time.Now()

	// 构造请求
	messages := []arkChatMessage{
		{Role: "user", Content: req.Prompt},
	}
	if req.SystemPrompt != "" {
		messages = append([]arkChatMessage{{Role: "system", Content: req.SystemPrompt}}, messages...)
	}

	// The deployment is chosen per operation: the console configures one per
	// task, and this is where that intent becomes real.
	endpoint := a.endpointFor(req.Operation)

	arkReq := arkChatRequest{
		Model:       endpoint,
		Messages:    messages,
		MaxTokens:   req.MaxTokens,
		Temperature: req.Temperature,
		Thinking:    map[string]any{"type": "disabled"}, // 关闭思考链避免计费
	}

	if req.ResponseFormat == "json_object" {
		arkReq.ResponseFormat = map[string]any{"type": "json_object"}
	}

	body, err := json.Marshal(arkReq)
	if err != nil {
		globalMetrics.incCompletionErrors()
		return CompletionResponse{}, fmt.Errorf("marshal request: %w", err)
	}

	// 调用 Ark API
	httpReq, err := http.NewRequestWithContext(ctx, "POST", a.baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		globalMetrics.incCompletionErrors()
		return CompletionResponse{}, fmt.Errorf("create request: %w", err)
	}

	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("Authorization", "Bearer "+a.apiKey)

	httpResp, err := a.httpClient.Do(httpReq)
	if err != nil {
		globalMetrics.incCompletionErrors()
		return CompletionResponse{}, fmt.Errorf("do request: %w", err)
	}
	defer func() { _ = httpResp.Body.Close() }()

	respBody, err := io.ReadAll(httpResp.Body)
	if err != nil {
		globalMetrics.incCompletionErrors()
		return CompletionResponse{}, fmt.Errorf("read response: %w", err)
	}

	if httpResp.StatusCode != http.StatusOK {
		globalMetrics.incCompletionErrors()
		return CompletionResponse{}, fmt.Errorf("ark api error: status=%d body=%s", httpResp.StatusCode, string(respBody))
	}

	// 解析响应
	var arkResp arkChatResponse
	if err := json.Unmarshal(respBody, &arkResp); err != nil {
		globalMetrics.incCompletionErrors()
		return CompletionResponse{}, fmt.Errorf("unmarshal response: %w", err)
	}

	if len(arkResp.Choices) == 0 {
		globalMetrics.incCompletionErrors()
		return CompletionResponse{}, fmt.Errorf("no choices in response")
	}

	latency := time.Since(start).Milliseconds()

	resp := CompletionResponse{
		Content:      arkResp.Choices[0].Message.Content,
		FinishReason: arkResp.Choices[0].FinishReason,
		PromptTokens: arkResp.Usage.PromptTokens,
		OutputTokens: arkResp.Usage.CompletionTokens,
		TotalTokens:  arkResp.Usage.TotalTokens,
		Model:        arkResp.Model,
		LatencyMS:    latency,
	}

	globalMetrics.incCompletions()

	// 自动写入成本记录
	if a.costWriter != nil && req.Operation != "" {
		costLog := CostLog{
			UserID:       req.UserID,
			Operation:    req.Operation,
			PromptTokens: resp.PromptTokens,
			OutputTokens: resp.OutputTokens,
			TotalTokens:  resp.TotalTokens,
			Model:        resp.Model,
			LatencyMS:    resp.LatencyMS,
		}
		if err := a.costWriter.Write(ctx, costLog); err != nil {
			globalMetrics.incCostWriteFailures()
		}
	}

	return resp, nil
}
