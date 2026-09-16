package drill_test

import (
	"context"
	"testing"

	"github.com/FluentWork/fluentwork-backend/internal/drill"
	"github.com/FluentWork/fluentwork-backend/internal/orchestrator"
)

func TestOrchestratorAdapter(t *testing.T) {
	mock := orchestrator.NewMockClient(`{"pass":true}`)
	adapter := &drill.OrchestratorAdapter{Client: mock}

	result, err := adapter.Complete(context.Background(), "test prompt")
	if err != nil {
		t.Fatalf("Complete failed: %v", err)
	}
	if result != `{"pass":true}` {
		t.Errorf("got %q, want %q", result, `{"pass":true}`)
	}

	if len(mock.Calls) != 1 {
		t.Fatalf("expected 1 call, got %d", len(mock.Calls))
	}
	call := mock.Calls[0]
	if call.Prompt != "test prompt" {
		t.Errorf("prompt: got %q, want %q", call.Prompt, "test prompt")
	}
	if call.MaxTokens != 200 {
		t.Errorf("MaxTokens: got %d, want 200", call.MaxTokens)
	}
	if call.Temperature != 0 {
		t.Errorf("Temperature: got %f, want 0", call.Temperature)
	}
	if call.ResponseFormat != "json_object" {
		t.Errorf("ResponseFormat: got %q, want json_object", call.ResponseFormat)
	}
	if call.Operation != "drill.judge" {
		t.Errorf("Operation: got %q, want drill.judge", call.Operation)
	}
}
