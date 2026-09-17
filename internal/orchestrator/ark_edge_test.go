package orchestrator

import (
	"context"
	"encoding/json"
	"errors"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/FluentWork/fluentwork-backend/internal/config"
)

type failingCostWriter struct {
	callCount int
	err       error
}

func (f *failingCostWriter) Write(_ context.Context, _ CostLog) error {
	f.callCount++
	return f.err
}

func TestArkClient_CostWriterFailure(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		resp := arkChatResponse{
			ID:      "test-id",
			Created: 1234567890,
			Model:   "doubao-pro-32k",
			Choices: []struct {
				Index   int `json:"index"`
				Message struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				} `json:"message"`
				FinishReason string `json:"finish_reason"`
			}{
				{
					Message: struct {
						Role    string `json:"role"`
						Content string `json:"content"`
					}{
						Role:    "assistant",
						Content: "response",
					},
					FinishReason: "stop",
				},
			},
			Usage: struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
				TotalTokens      int `json:"total_tokens"`
			}{
				PromptTokens:     100,
				CompletionTokens: 50,
				TotalTokens:      150,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	costWriter := &failingCostWriter{err: errors.New("db connection lost")}
	client := &ArkClient{
		baseURL:    server.URL,
		apiKey:     "test-key",
		model:      "doubao-pro-32k",
		costWriter: costWriter,
		httpClient: http.DefaultClient,
	}

	GetMetrics().Reset()
	initialFailures := GetMetrics().CostWriteFailuresTotal

	resp, err := client.Complete(context.Background(), CompletionRequest{
		Prompt:    "test",
		Operation: "test.op",
	})
	if err != nil {
		t.Fatalf("Complete should succeed even when cost write fails: %v", err)
	}
	if resp.Content != "response" {
		t.Errorf("expected 'response', got %s", resp.Content)
	}

	if costWriter.callCount != 1 {
		t.Errorf("cost writer should be called once, got %d calls", costWriter.callCount)
	}

	metrics := GetMetrics()
	if metrics.CostWriteFailuresTotal != initialFailures+1 {
		t.Errorf("CostWriteFailuresTotal should increment, got %d", metrics.CostWriteFailuresTotal)
	}
}

func TestArkClient_NilCostWriter(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		resp := arkChatResponse{
			ID:      "test-id",
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
					Message: struct {
						Role    string `json:"role"`
						Content string `json:"content"`
					}{
						Role:    "assistant",
						Content: "ok",
					},
				},
			},
			Usage: struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
				TotalTokens      int `json:"total_tokens"`
			}{
				PromptTokens:     10,
				CompletionTokens: 10,
				TotalTokens:      20,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := &ArkClient{
		baseURL:    server.URL,
		apiKey:     "test-key",
		model:      "test-model",
		costWriter: nil,
		httpClient: http.DefaultClient,
	}

	resp, err := client.Complete(context.Background(), CompletionRequest{
		Prompt:    "test",
		Operation: "test.op",
	})
	if err != nil {
		t.Fatalf("Complete should handle nil cost writer gracefully: %v", err)
	}
	if resp.Content != "ok" {
		t.Errorf("expected 'ok', got %s", resp.Content)
	}
}

func TestArkClient_HTTPError(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.WriteHeader(http.StatusInternalServerError)
		_, _ = w.Write([]byte(`{"error": {"message": "internal error"}}`))
	}))
	defer server.Close()

	client := &ArkClient{
		baseURL:    server.URL,
		apiKey:     "test-key",
		model:      "test-model",
		costWriter: &mockCostWriter{},
		httpClient: http.DefaultClient,
	}

	_, err := client.Complete(context.Background(), CompletionRequest{
		Prompt:    "test",
		Operation: "test.op",
	})

	if err == nil {
		t.Fatal("expected error for 500 response")
	}
	if !strings.Contains(err.Error(), "500") {
		t.Errorf("error should mention status code, got: %v", err)
	}
}

func TestArkClient_MalformedJSON(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"choices": [{"message": {"content": "incomplete`))
	}))
	defer server.Close()

	client := &ArkClient{
		baseURL:    server.URL,
		apiKey:     "test-key",
		model:      "test-model",
		costWriter: &mockCostWriter{},
		httpClient: http.DefaultClient,
	}

	_, err := client.Complete(context.Background(), CompletionRequest{
		Prompt:    "test",
		Operation: "test.op",
	})

	if err == nil {
		t.Fatal("expected error for malformed JSON")
	}
}

func TestArkClient_EmptyChoices(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
		resp := arkChatResponse{
			ID:      "test-id",
			Created: 1234567890,
			Model:   "test-model",
			Choices: []struct {
				Index   int `json:"index"`
				Message struct {
					Role    string `json:"role"`
					Content string `json:"content"`
				} `json:"message"`
				FinishReason string `json:"finish_reason"`
			}{},
			Usage: struct {
				PromptTokens     int `json:"prompt_tokens"`
				CompletionTokens int `json:"completion_tokens"`
				TotalTokens      int `json:"total_tokens"`
			}{
				PromptTokens:     10,
				CompletionTokens: 0,
				TotalTokens:      10,
			},
		}
		w.Header().Set("Content-Type", "application/json")
		_ = json.NewEncoder(w).Encode(resp)
	}))
	defer server.Close()

	client := &ArkClient{
		baseURL:    server.URL,
		apiKey:     "test-key",
		model:      "test-model",
		costWriter: &mockCostWriter{},
		httpClient: http.DefaultClient,
	}

	_, err := client.Complete(context.Background(), CompletionRequest{
		Prompt:    "test",
		Operation: "test.op",
	})

	if err == nil {
		t.Fatal("expected error for empty choices")
	}
	if !strings.Contains(err.Error(), "no choices") {
		t.Errorf("error should mention empty choices, got: %v", err)
	}
}

func TestArkClient_ContextCancellation(t *testing.T) {
	server := httptest.NewServer(http.HandlerFunc(func(_ http.ResponseWriter, r *http.Request) {
		<-r.Context().Done()
	}))
	defer server.Close()

	client := &ArkClient{
		baseURL:    server.URL,
		apiKey:     "test-key",
		model:      "test-model",
		costWriter: &mockCostWriter{},
		httpClient: http.DefaultClient,
	}

	ctx, cancel := context.WithCancel(context.Background())
	cancel()

	_, err := client.Complete(ctx, CompletionRequest{
		Prompt:    "test",
		Operation: "test.op",
	})

	if err == nil {
		t.Fatal("expected error for cancelled context")
	}
	if !errors.Is(err, context.Canceled) {
		t.Errorf("expected context.Canceled, got: %v", err)
	}
}

func TestNewArkClient_ValidatesConfig(t *testing.T) {
	tests := []struct {
		name   string
		cfg    config.Config
		assert func(*testing.T, *ArkClient)
	}{
		{
			name: "uses default base URL when empty",
			cfg: config.Config{
				ArkBaseURL:        "",
				ArkAPIKey:         "key",
				ArkReviewRefineEP: "model",
			},
			assert: func(t *testing.T, c *ArkClient) {
				if c.baseURL != "https://ark.cn-beijing.volces.com/api/v3" {
					t.Errorf("expected default base URL, got %s", c.baseURL)
				}
			},
		},
		{
			name: "preserves custom base URL",
			cfg: config.Config{
				ArkBaseURL:        "https://custom.example.com",
				ArkAPIKey:         "key",
				ArkReviewRefineEP: "model",
			},
			assert: func(t *testing.T, c *ArkClient) {
				if c.baseURL != "https://custom.example.com" {
					t.Errorf("expected custom base URL, got %s", c.baseURL)
				}
			},
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			client := NewArkClient(tt.cfg, nil)
			tt.assert(t, client)
		})
	}
}
