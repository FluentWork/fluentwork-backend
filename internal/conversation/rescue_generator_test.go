package conversation

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// MockLLMClient 用于测试的 LLM 客户端 mock
type MockLLMClient struct {
	CompleteFunc func(ctx context.Context, prompt string, options CompletionOptions) (string, error)
}

func (m *MockLLMClient) Complete(ctx context.Context, prompt string, options CompletionOptions) (string, error) {
	if m.CompleteFunc != nil {
		return m.CompleteFunc(ctx, prompt, options)
	}
	return "", errors.New("CompleteFunc not implemented")
}

func TestRescueGenerator_GenerateSkeleton(t *testing.T) {
	mockClient := &MockLLMClient{
		CompleteFunc: func(_ context.Context, _ string, _ CompletionOptions) (string, error) {
			// 模拟返回句首骨架
			return "I think the main risk is...", nil
		},
	}

	generator := NewRescueGenerator(mockClient)
	ctx := context.Background()

	conv := ConversationContext{
		LastAIMessage:   "What's the biggest risk in this design?",
		ScenarioContext: "Database migration strategy discussion",
		UserRole:        "Backend Engineer",
	}

	result, err := generator.GenerateRescue(ctx, RescueSkeleton, conv)
	if err != nil {
		t.Fatalf("GenerateSkeleton failed: %v", err)
	}

	if result == "" {
		t.Error("Expected non-empty skeleton, got empty string")
	}

	t.Logf("Generated skeleton: %s", result)
}

func TestRescueGenerator_GenerateHint(t *testing.T) {
	mockClient := &MockLLMClient{
		CompleteFunc: func(_ context.Context, _ string, _ CompletionOptions) (string, error) {
			// 模拟返回中文提示
			return "先说结论，再说原因", nil
		},
	}

	generator := NewRescueGenerator(mockClient)
	ctx := context.Background()

	conv := ConversationContext{
		LastAIMessage:   "Why do you prefer this approach?",
		ScenarioContext: "API design review",
		UserRole:        "Backend Engineer",
	}

	result, err := generator.GenerateRescue(ctx, RescueHint, conv)
	if err != nil {
		t.Fatalf("GenerateHint failed: %v", err)
	}

	if result == "" {
		t.Error("Expected non-empty hint, got empty string")
	}

	t.Logf("Generated hint: %s", result)
}

func TestRescueGenerator_GenerateComplete(t *testing.T) {
	mockClient := &MockLLMClient{
		CompleteFunc: func(_ context.Context, _ string, _ CompletionOptions) (string, error) {
			// 模拟返回完整表达
			return "I think we should use a blue-green deployment strategy to minimize downtime.", nil
		},
	}

	generator := NewRescueGenerator(mockClient)
	ctx := context.Background()

	conv := ConversationContext{
		LastAIMessage:   "How should we handle the deployment?",
		ScenarioContext: "Production deployment planning",
		UserRole:        "Backend Engineer",
		RecentTurns: []Turn{
			{Speaker: "ai", Content: "What's your concern about the current approach?"},
			{Speaker: "user", Content: "It might cause downtime."},
		},
	}

	result, err := generator.GenerateRescue(ctx, RescueComplete, conv)
	if err != nil {
		t.Fatalf("GenerateComplete failed: %v", err)
	}

	if result == "" {
		t.Error("Expected non-empty complete response, got empty string")
	}

	t.Logf("Generated complete response: %s", result)
}

func TestRescueGenerator_InvalidLevel(t *testing.T) {
	mockClient := &MockLLMClient{}
	generator := NewRescueGenerator(mockClient)
	ctx := context.Background()

	conv := ConversationContext{
		LastAIMessage: "Test question",
	}

	_, err := generator.GenerateRescue(ctx, RescueLevel(99), conv)
	if err == nil {
		t.Error("Expected error for invalid rescue level, got nil")
	}
}

func TestRescueGenerator_LLMError(t *testing.T) {
	mockClient := &MockLLMClient{
		CompleteFunc: func(_ context.Context, _ string, _ CompletionOptions) (string, error) {
			return "", errors.New("LLM service unavailable")
		},
	}

	generator := NewRescueGenerator(mockClient)
	ctx := context.Background()

	conv := ConversationContext{
		LastAIMessage: "Test question",
	}

	_, err := generator.GenerateRescue(ctx, RescueSkeleton, conv)
	if err == nil {
		t.Error("Expected error when LLM fails, got nil")
	}
}

// The ladder is spoken by someone who is already stuck, so every rung carries a
// length ceiling — and the ceiling has to live in the request as well as the
// instructions. The first real-device run came back with a 50-word "complete
// sentence": the prompt asked for 12 words, the token budget allowed 80, and
// the budget is what the model actually obeyed.
//
// This test is written against the prompt text because the prompt is the
// product here. There is no assertion that a model obeys it — that is what the
// eval suite is for — only that nobody deletes the ceiling without noticing.
func TestRescueGenerator_RungsCarryALengthCeiling(t *testing.T) {
	cases := []struct {
		name  string
		level RescueLevel
		// ceilingMarker is how this rung states its own limit: English rungs
		// count words, the Chinese hint counts characters.
		ceilingMarker string
		// maxTokens is the ceiling this rung must not exceed: ~2 tokens per
		// word of English plus slack, so a 12-word rung cannot outgrow itself.
		maxTokens int
	}{
		{"skeleton", RescueSkeleton, "words", 60},
		{"hint", RescueHint, "字", 60},
		{"complete", RescueComplete, "words", 60},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			var gotPrompt string
			var gotOptions CompletionOptions
			gen := NewRescueGenerator(&MockLLMClient{
				CompleteFunc: func(_ context.Context, prompt string, options CompletionOptions) (string, error) {
					gotPrompt, gotOptions = prompt, options
					return "I think the main risk is...", nil
				},
			})
			if _, err := gen.GenerateRescue(context.Background(), tc.level, ConversationContext{
				LastAIMessage: "What is blocking the release?",
			}); err != nil {
				t.Fatalf("GenerateRescue: %v", err)
			}
			if !strings.Contains(gotPrompt, tc.ceilingMarker) {
				t.Fatalf("prompt states no length ceiling (%q):\n%s", tc.ceilingMarker, gotPrompt)
			}
			if gotOptions.MaxTokens > tc.maxTokens {
				t.Fatalf("MaxTokens = %d: a budget this large lets the rung outgrow the ceiling the prompt states",
					gotOptions.MaxTokens)
			}
			if gotOptions.MaxTokens == 0 {
				t.Fatal("MaxTokens unset: the ceiling would be whatever the provider defaults to")
			}
		})
	}
}
