package tts

import (
	"context"
	"errors"
	"testing"
)

func TestRouter_Stream_RouteHit(t *testing.T) {
	mockA := &MockProvider{
		streamFunc: func(ctx context.Context, text string, voice VoiceConfig) (<-chan AudioChunk, error) {
			ch := make(chan AudioChunk, 1)
			ch <- AudioChunk{Data: []byte("mock-a"), Seq: 0, IsFinal: true}
			close(ch)
			return ch, nil
		},
	}
	mockB := &MockProvider{
		streamFunc: func(ctx context.Context, text string, voice VoiceConfig) (<-chan AudioChunk, error) {
			ch := make(chan AudioChunk, 1)
			ch <- AudioChunk{Data: []byte("mock-b"), Seq: 0, IsFinal: true}
			close(ch)
			return ch, nil
		},
	}

	router := NewRouter(map[string]Provider{
		"voice-a": mockA,
		"voice-b": mockB,
	}, nil)

	ctx := context.Background()

	ch, err := router.Stream(ctx, "test", VoiceConfig{VoiceID: "voice-a"})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	chunk := <-ch
	if string(chunk.Data) != "mock-a" {
		t.Errorf("expected mock-a, got %s", chunk.Data)
	}

	ch, err = router.Stream(ctx, "test", VoiceConfig{VoiceID: "voice-b"})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	chunk = <-ch
	if string(chunk.Data) != "mock-b" {
		t.Errorf("expected mock-b, got %s", chunk.Data)
	}

	if processMetrics.RouteHits("voice-a") != 1 {
		t.Errorf("expected 1 hit for voice-a, got %d", processMetrics.RouteHits("voice-a"))
	}
	if processMetrics.RouteHits("voice-b") != 1 {
		t.Errorf("expected 1 hit for voice-b, got %d", processMetrics.RouteHits("voice-b"))
	}
}

func TestRouter_Stream_RouteMiss_Fallback(t *testing.T) {
	mockFallback := &MockProvider{
		streamFunc: func(ctx context.Context, text string, voice VoiceConfig) (<-chan AudioChunk, error) {
			ch := make(chan AudioChunk, 1)
			ch <- AudioChunk{Data: []byte("fallback"), Seq: 0, IsFinal: true}
			close(ch)
			return ch, nil
		},
	}

	router := NewRouter(map[string]Provider{
		"voice-a": &MockProvider{},
	}, mockFallback)

	beforeMisses := processMetrics.RouteMisses()

	ctx := context.Background()
	ch, err := router.Stream(ctx, "test", VoiceConfig{VoiceID: "unknown-voice"})
	if err != nil {
		t.Fatalf("expected no error, got %v", err)
	}
	chunk := <-ch
	if string(chunk.Data) != "fallback" {
		t.Errorf("expected fallback, got %s", chunk.Data)
	}

	if processMetrics.RouteMisses() != beforeMisses+1 {
		t.Errorf("expected route miss increment")
	}
}

func TestRouter_Stream_RouteMiss_NoFallback(t *testing.T) {
	router := NewRouter(map[string]Provider{
		"voice-a": &MockProvider{},
	}, nil)

	beforeMisses := processMetrics.RouteMisses()

	ctx := context.Background()
	_, err := router.Stream(ctx, "test", VoiceConfig{VoiceID: "unknown-voice"})
	if !errors.Is(err, ErrClosed) {
		t.Errorf("expected ErrClosed, got %v", err)
	}

	if processMetrics.RouteMisses() != beforeMisses+1 {
		t.Errorf("expected route miss increment")
	}
}

func TestRouter_Stream_NilRouter(t *testing.T) {
	var router *Router
	ctx := context.Background()
	_, err := router.Stream(ctx, "test", VoiceConfig{VoiceID: "voice-a"})
	if !errors.Is(err, ErrClosed) {
		t.Errorf("expected ErrClosed for nil router, got %v", err)
	}
}

func TestRouter_Ping_AllProviders(t *testing.T) {
	mockA := &MockProvider{
		pingFunc: func(ctx context.Context) error {
			return nil
		},
	}
	mockB := &MockProvider{
		pingFunc: func(ctx context.Context) error {
			return nil
		},
	}
	mockFallback := &MockProvider{
		pingFunc: func(ctx context.Context) error {
			return nil
		},
	}

	router := NewRouter(map[string]Provider{
		"voice-a": mockA,
		"voice-b": mockB,
	}, mockFallback)

	ctx := context.Background()
	if err := router.Ping(ctx); err != nil {
		t.Errorf("expected no error, got %v", err)
	}
}

func TestRouter_Ping_FirstError(t *testing.T) {
	errTest := errors.New("ping failed")
	mockA := &MockProvider{
		pingFunc: func(ctx context.Context) error {
			return errTest
		},
	}
	mockB := &MockProvider{
		pingFunc: func(ctx context.Context) error {
			return nil
		},
	}

	router := NewRouter(map[string]Provider{
		"voice-a": mockA,
		"voice-b": mockB,
	}, nil)

	ctx := context.Background()
	err := router.Ping(ctx)
	if !errors.Is(err, errTest) {
		t.Errorf("expected ping error, got %v", err)
	}
}

func TestRouter_Close_AllProviders(t *testing.T) {
	closedA := false
	closedB := false
	closedFallback := false

	mockA := &MockProvider{
		closeFunc: func() error {
			closedA = true
			return nil
		},
	}
	mockB := &MockProvider{
		closeFunc: func() error {
			closedB = true
			return nil
		},
	}
	mockFallback := &MockProvider{
		closeFunc: func() error {
			closedFallback = true
			return nil
		},
	}

	router := NewRouter(map[string]Provider{
		"voice-a": mockA,
		"voice-b": mockB,
	}, mockFallback)

	if err := router.Close(); err != nil {
		t.Errorf("expected no error, got %v", err)
	}

	if !closedA || !closedB || !closedFallback {
		t.Errorf("expected all providers closed, got A=%v B=%v fallback=%v", closedA, closedB, closedFallback)
	}
}

func TestRouter_Close_FirstError(t *testing.T) {
	errTest := errors.New("close failed")
	mockA := &MockProvider{
		closeFunc: func() error {
			return errTest
		},
	}
	mockB := &MockProvider{
		closeFunc: func() error {
			return nil
		},
	}

	router := NewRouter(map[string]Provider{
		"voice-a": mockA,
		"voice-b": mockB,
	}, nil)

	err := router.Close()
	if !errors.Is(err, errTest) {
		t.Errorf("expected close error, got %v", err)
	}
}

// MockProvider for testing
type MockProvider struct {
	streamFunc func(ctx context.Context, text string, voice VoiceConfig) (<-chan AudioChunk, error)
	pingFunc   func(ctx context.Context) error
	closeFunc  func() error
}

func (m *MockProvider) Stream(ctx context.Context, text string, voice VoiceConfig) (<-chan AudioChunk, error) {
	if m.streamFunc != nil {
		return m.streamFunc(ctx, text, voice)
	}
	ch := make(chan AudioChunk)
	close(ch)
	return ch, nil
}

func (m *MockProvider) Ping(ctx context.Context) error {
	if m.pingFunc != nil {
		return m.pingFunc(ctx)
	}
	return nil
}

func (m *MockProvider) Close() error {
	if m.closeFunc != nil {
		return m.closeFunc()
	}
	return nil
}
