package orchestrator

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/FluentWork/fluentwork-backend/internal/config"
)

type mockCostWriter struct {
	logs []CostLog
}

func (m *mockCostWriter) Write(ctx context.Context, log CostLog) error {
	m.logs = append(m.logs, log)
	return nil
}

func TestArkClient_Complete(t *testing.T) {
	// 创建测试服务器
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 验证请求
		if r.Method != "POST" {
			t.Errorf("expected POST, got %s", r.Method)
		}
		if r.Header.Get("Authorization") != "Bearer test-key" {
			t.Errorf("expected Bearer token, got %s", r.Header.Get("Authorization"))
		}
		
		// 返回模拟响应
		resp := arkChatResponse{
			ID:      "test-id",
			Object:  "chat.completion",
			Created: 1234567890,
			Model:   "test-model",
			Choices: []struct {
				Index   int `json:"index"`
				Message struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				} `json:"message"`
				FinishReason string `json:"finish_reason"`
			}{
				{
					Index: 0,
					Message: struct {
						Role    string `json:"role"`
						Content string `json:"content"`
					}{
						Role:    "assistant",
						Content: "test response",
					},
					FinishReason: "stop",
				},
			},
			Usage: struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
				TotalTokens      int `json:"total_tokens"`
			}{
				PromptTokens:     10,
				CompletionTokens: 20,
				TotalTokens:      30,
			},
		}
		
		w.Header().Set("Content-Type", "application/json")
		json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()
	
	// 创建客户端
	costWriter := &mockCostWriter{}
	client := &ArkClient{
		baseURL:    server.URL,
		apiKey:     "test-key",
		model:      "test-model",
		costWriter: costWriter,
		httpClient: http.DefaultClient,
	}
	
	// 调用 Complete
	resp, err := client.Complete(context.Background(), CompletionRequest{
		Prompt:      "test prompt",
		MaxTokens:   100,
		Temperature: 0.7,
		Operation:   "test.operation",
	})
	
	// 验证响应
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "test response" {
		t.Errorf("expected 'test response', got %s", resp.Content)
	}
	if resp.PromptTokens != 10 {
		t.Errorf("expected 10 prompt tokens, got %d", resp.PromptTokens)
	}
	if resp.OutputTokens != 20 {
		t.Errorf("expected 20 output tokens, got %d", resp.OutputTokens)
	}
	if resp.TotalTokens != 30 {
		t.Errorf("expected 30 total tokens, got %d", resp.TotalTokens)
	}
	
	// 验证成本记录
	if len(costWriter.logs) != 1 {
		t.Fatalf("expected 1 cost log, got %d", len(costWriter.logs))
	}
	log := costWriter.logs[0]
	if log.Operation != "test.operation" {
		t.Errorf("expected operation 'test.operation', got %s", log.Operation)
	}
	if log.TotalTokens != 30 {
		t.Errorf("expected 30 total tokens in cost log, got %d", log.TotalTokens)
	}
}

func TestNewClient(t *testing.T) {
	cfg := config.Config{
		ArkBaseURL:        "https://ark.example.com",
		ArkAPIKey:         "test-key",
		ArkReviewRefineEP: "test-model",
	}
	
	client := NewClient(cfg, nil)
	if client == nil {
		t.Fatal("expected non-nil client")
	}
	
	// 验证类型
	if _, ok := client.(*ArkClient); !ok {
		t.Errorf("expected *ArkClient, got %T", client)
	}
}
