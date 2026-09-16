package review

import (
	"context"
	"errors"
	"testing"

	"github.com/FluentWork/fluentwork-backend/internal/orchestrator"
)

func TestOrchestratorAdapter_Complete(t *testing.T) {
	client := orchestrator.NewMockClient(`{"score": 0.85, "dims": {"grammar": 0.9, "fluency": 0.8, "vocabulary": 0.85}, "suggestions": ["Good job"]}`)
	adapter := &OrchestratorAdapter{Client: client}

	result, err := adapter.Complete(context.Background(), "test prompt")
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}

	expected := `{"score": 0.85, "dims": {"grammar": 0.9, "fluency": 0.8, "vocabulary": 0.85}, "suggestions": ["Good job"]}`
	if result != expected {
		t.Errorf("expected %q, got %q", expected, result)
	}
}

func TestOrchestratorAdapter_CompleteError(t *testing.T) {
	expectedErr := errors.New("mock error")
	client := orchestrator.NewMockClientWithError(expectedErr)
	adapter := &OrchestratorAdapter{Client: client}

	_, err := adapter.Complete(context.Background(), "test prompt")
	if err != expectedErr {
		t.Errorf("expected error %v, got %v", expectedErr, err)
	}
}
