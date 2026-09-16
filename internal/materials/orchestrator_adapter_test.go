package materials_test

import (
	"context"
	"testing"

	"github.com/FluentWork/fluentwork-backend/internal/materials"
	"github.com/FluentWork/fluentwork-backend/internal/orchestrator"
)

func TestOrchestratorAdapter(t *testing.T) {
	t.Run("success", func(t *testing.T) {
		mock := orchestrator.NewMockClient("refined material")
		adapter := &materials.OrchestratorAdapter{Client: mock}

		got, err := adapter.Complete(context.Background(), "refine this")
		if err != nil {
			t.Fatalf("Complete() error = %v", err)
		}
		if got != "refined material" {
			t.Errorf("Complete() = %q, want %q", got, "refined material")
		}

		if len(mock.Calls) != 1 {
			t.Fatalf("expected 1 call, got %d", len(mock.Calls))
		}
		call := mock.Calls[0]
		if call.Prompt != "refine this" {
			t.Errorf("Prompt = %q, want %q", call.Prompt, "refine this")
		}
		if call.Operation != "materials.refine" {
			t.Errorf("Operation = %q, want %q", call.Operation, "materials.refine")
		}
	})
}
