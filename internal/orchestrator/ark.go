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

// ArkClient Ark LLM 客户端，基于 drill.ArkCompleter 迁移并增强
type ArkClient struct {
	baseURL    string
	apiKey     string
	model      string
	costWriter CostWriter
	httpClient *http.Client
}

// NewArkClient 创建 Ark 客户端
func NewArkClient(cfg config.Config, costWriter CostWriter) *ArkClient {
	model := strings.TrimSpace(cfg.ArkReviewRefineEP)
	
	return &ArkClient{
		baseURL:    cfg.ArkBaseURL,
		apiKey:     cfg.ArkAPIKey,
		model:      model,
		costWriter: costWriter,
		httpClient: &http.Client{
			Timeout: 30 * time.Second,
			Transport: &http.Transport{
				DialContext: (&net.Dialer{
					Timeout:   5 * time.Second,
					KeepAlive: 30 * time.Second,
				}).DialContext,
				MaxIdleConns:        100,
				IdleConnTimeout:     90 * time.Second,
				TLSHandshakeTimeout: 5 * time.Second,
			},
		},
	}
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
	
	arkReq := arkChatRequest{
		Model:       a.model,
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
	defer httpResp.Body.Close()
	
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
