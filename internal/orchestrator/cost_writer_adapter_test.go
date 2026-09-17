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
		Model:        "doubao-mini-32k", // a model the built-in table prices
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
	// doubao-mini-32k: 3分/1K input, 6分/1K output
	// Cost = 1000*3/1000 + 500*6/1000 = 3 + 3 = 6 分
	expectedCost := 6
	if recorded.CostFen != expectedCost {
		t.Errorf("CostFen = %d; want %d (doubao-mini-32k: 1000*3/1000 + 500*6/1000)", recorded.CostFen, expectedCost)
	}
}

// The deployed model (probed, not guessed) has no built-in price: the row is
// written with usage facts and cost_fen = 0, and the model shows up in
// UnpricedModels so somebody can add it to the pricing file.
func TestAICostWriterAdapter_UnpricedDeployedModelRecordsUsage(t *testing.T) {
	store := aicost.NewMemoryStore()
	adapter := NewAICostWriterAdapter(aicost.NewService(store, nil))
	globalMetrics.Reset()
	t.Cleanup(globalMetrics.Reset)

	if err := adapter.Write(context.Background(), CostLog{
		UserID:       "user-cost-test",
		Operation:    "review.eval",
		Model:        "ep-20260830204651-pffhf", // → doubao-seed-2-1-pro-260628
		PromptTokens: 1000,
		OutputTokens: 500,
	}); err != nil {
		t.Fatalf("Write: %v", err)
	}

	logs, err := store.ListRecent(context.Background(), "user-cost-test", 10)
	if err != nil {
		t.Fatalf("ListRecent: %v", err)
	}
	if len(logs) != 1 {
		t.Fatalf("logs = %+v", logs)
	}
	if logs[0].CostFen != 0 {
		t.Fatalf("cost_fen = %d, want 0 for a model with no rate", logs[0].CostFen)
	}
	if logs[0].TokensIn != 1000 || logs[0].TokensOut != 500 {
		t.Fatalf("usage must still be recorded: %+v", logs[0])
	}
	metrics := GetMetrics()
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	if metrics.UnpricedModels["ep-20260830204651-pffhf"] != 1 {
		t.Fatalf("unpriced = %+v", metrics.UnpricedModels)
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
