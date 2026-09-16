package orchestrator

import (
	"testing"
)

func TestMetrics_Increment(t *testing.T) {
	m := &Metrics{}
	
	m.incCostWriteFailures()
	m.incCostWriteFailures()
	m.incCompletions()
	m.incCompletionErrors()
	
	if m.CostWriteFailuresTotal != 2 {
		t.Errorf("CostWriteFailuresTotal = %d, want 2", m.CostWriteFailuresTotal)
	}
	if m.CompletionsTotal != 1 {
		t.Errorf("CompletionsTotal = %d, want 1", m.CompletionsTotal)
	}
	if m.CompletionErrorsTotal != 1 {
		t.Errorf("CompletionErrorsTotal = %d, want 1", m.CompletionErrorsTotal)
	}
}

func TestMetrics_Reset(t *testing.T) {
	m := &Metrics{}
	
	m.incCostWriteFailures()
	m.incCompletions()
	m.incCompletionErrors()
	
	m.Reset()
	
	if m.CostWriteFailuresTotal != 0 {
		t.Errorf("after reset, CostWriteFailuresTotal = %d, want 0", m.CostWriteFailuresTotal)
	}
	if m.CompletionsTotal != 0 {
		t.Errorf("after reset, CompletionsTotal = %d, want 0", m.CompletionsTotal)
	}
	if m.CompletionErrorsTotal != 0 {
		t.Errorf("after reset, CompletionErrorsTotal = %d, want 0", m.CompletionErrorsTotal)
	}
}

func TestGetMetrics(t *testing.T) {
	globalMetrics.Reset()
	
	m := GetMetrics()
	if m != globalMetrics {
		t.Errorf("GetMetrics() returned different instance")
	}
	
	m.incCompletions()
	
	m2 := GetMetrics()
	if m2.CompletionsTotal != 1 {
		t.Errorf("global metrics not shared: CompletionsTotal = %d, want 1", m2.CompletionsTotal)
	}
	
	globalMetrics.Reset()
}
