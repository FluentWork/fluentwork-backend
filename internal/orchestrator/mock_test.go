package orchestrator

import (
	"context"
	"errors"
	"testing"
)

func TestMockClient_Complete(t *testing.T) {
	client := NewMockClient("mock response")
	
	resp, err := client.Complete(context.Background(), CompletionRequest{
		Prompt: "test",
	})
	
	if err != nil {
		t.Fatalf("unexpected error: %v", err)
	}
	if resp.Content != "mock response" {
		t.Errorf("expected 'mock response', got %s", resp.Content)
	}
	if resp.Model != "mock-model" {
		t.Errorf("expected 'mock-model', got %s", resp.Model)
	}
}

func TestMockClient_WithError(t *testing.T) {
	expectedErr := errors.New("mock error")
	client := NewMockClientWithError(expectedErr)
	
	_, err := client.Complete(context.Background(), CompletionRequest{
		Prompt: "test",
	})
	
	if err != expectedErr {
		t.Errorf("expected error %v, got %v", expectedErr, err)
	}
}

func TestMockClient_RecordsCalls(t *testing.T) {
	client := &MockClient{
		Response: CompletionResponse{Content: "test"},
		Calls:    []CompletionRequest{},
	}
	
	req1 := CompletionRequest{Prompt: "first"}
	req2 := CompletionRequest{Prompt: "second"}
	
	client.Complete(context.Background(), req1)
	client.Complete(context.Background(), req2)
	
	if len(client.Calls) != 2 {
		t.Fatalf("expected 2 calls, got %d", len(client.Calls))
	}
	if client.Calls[0].Prompt != "first" {
		t.Errorf("expected first prompt, got %s", client.Calls[0].Prompt)
	}
	if client.Calls[1].Prompt != "second" {
		t.Errorf("expected second prompt, got %s", client.Calls[1].Prompt)
	}
}
