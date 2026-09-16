package topic_test

import (
	"context"
	"testing"

	"github.com/FluentWork/fluentwork-backend/internal/orchestrator"
	"github.com/FluentWork/fluentwork-backend/internal/topic"
)

func TestOrchestratorAdapter(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		mock := orchestrator.NewMockClient(`{"cards": []}`)
		adapter := &topic.OrchestratorAdapter{Client: mock}

		got, err := adapter.Complete(context.Background(), "generate topics")
		if err != nil {
			t.Fatalf("Complete() error = %v", err)
		}
		if got != `{"cards": []}` {
			t.Errorf("Complete() = %q, want %q", got, `{"cards": []}`)
		}

		if len(mock.Calls) != 1 {
			t.Fatalf("expected 1 call, got %d", len(mock.Calls))
		}
		call := mock.Calls[0]
		if call.Prompt != "generate topics" {
			t.Errorf("Prompt = %q, want %q", call.Prompt, "generate topics")
		}
		if call.Operation != "topic.generate" {
			t.Errorf("Operation = %q, want %q", call.Operation, "topic.generate")
		}
		if call.Temperature != 0.7 {
			t.Errorf("Temperature = %f, want 0.7", call.Temperature)
		}
		if call.MaxTokens != 800 {
			t.Errorf("MaxTokens = %d, want 800", call.MaxTokens)
		}
	})
}
