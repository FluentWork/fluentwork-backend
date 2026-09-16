package orchestrator

import (
	"context"
	"testing"

	"github.com/FluentWork/fluentwork-backend/internal/aicost"
)

func TestAICostWriterAdapter_Write(t *testing.T) {
	store := aicost.NewMemoryStore()
	service := aicost.NewService(store, nil)
	adapter := NewAICostWriterAdapter(service)

	log := CostLog{
		UserID:       "user-123",
		SessionID:    "sess-456",
		Operation:    "review.eval",
		PromptTokens: 100,
		OutputTokens: 200,
		TotalTokens:  300,
		Model:        "doubao-pro-32k",
		LatencyMS:    500,
	}

	err := adapter.Write(context.Background(), log)
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	logs, err := store.ListRecent(context.Background(), "user-123", 10)
	if err != nil {
		t.Fatalf("ListRecent failed: %v", err)
	}

	if len(logs) != 1 {
		t.Fatalf("expected 1 log, got %d", len(logs))
	}

	recorded := logs[0]
	if recorded.TaskType != "review.eval" {
		t.Errorf("expected task_type 'review.eval', got %q", recorded.TaskType)
	}
	if recorded.Model != "doubao-pro-32k" {
		t.Errorf("expected model 'doubao-pro-32k', got %q", recorded.Model)
	}
	if recorded.TokensIn != 100 {
		t.Errorf("expected tokens_in 100, got %d", recorded.TokensIn)
	}
	if recorded.TokensOut != 200 {
		t.Errorf("expected tokens_out 200, got %d", recorded.TokensOut)
	}
	if recorded.CostFen <= 0 {
		t.Errorf("expected CostFen > 0, got %d", recorded.CostFen)
	}
}

func TestAICostWriterAdapter_CalculatesCost(t *testing.T) {
	store := aicost.NewMemoryStore()
	service := aicost.NewService(store, nil)
	adapter := NewAICostWriterAdapter(service)

	log := CostLog{
		UserID:       "user-cost-test",
		Operation:    "review.eval",
		Model:        "ep-20260830204651-pffhf",
		PromptTokens: 1000,
		OutputTokens: 500,
	}

	err := adapter.Write(context.Background(), log)
	if err != nil {
		t.Fatalf("Write failed: %v", err)
	}

	logs, err := store.ListRecent(context.Background(), "user-cost-test", 10)
	if err != nil {
		t.Fatalf("ListRecent failed: %v", err)
	}

	if len(logs) != 1 {
		t.Fatalf("expected 1 log, got %d", len(logs))
	}

	recorded := logs[0]
	expectedCost := 9
	if recorded.CostFen != expectedCost {
		t.Errorf("CostFen = %d; want %d (1000*5/1000 + 500*9/1000)", recorded.CostFen, expectedCost)
	}
}

func TestAICostWriterAdapter_NilService(t *testing.T) {
	adapter := &AICostWriterAdapter{service: nil}

	log := CostLog{
		Operation:    "test.op",
		PromptTokens: 10,
		OutputTokens: 20,
		Model:        "test-model",
	}

	err := adapter.Write(context.Background(), log)
	if err != nil {
		t.Errorf("expected nil service to be graceful, got error: %v", err)
	}
}
