package tts

import (
	"context"
	"errors"
	"strings"
	"testing"
)

type stubProvider struct {
	err     error
	chunks  []AudioChunk
	streams int
}

func (s *stubProvider) Stream(context.Context, string, VoiceConfig) (<-chan AudioChunk, error) {
	s.streams++
	if s.err != nil {
		return nil, s.err
	}
	out := make(chan AudioChunk, len(s.chunks))
	for _, chunk := range s.chunks {
		out <- chunk
	}
	close(out)
	return out, nil
}

func (s *stubProvider) Ping(context.Context) error { return nil }

func (s *stubProvider) Close() error { return nil }

func TestManager_FallbackTrigger(t *testing.T) {
	primary := &stubProvider{err: &httpStatusError{Status: 503, LogID: "up"}}
	fallback := &stubProvider{chunks: []AudioChunk{
		{Data: []byte("duplex"), Seq: 0, IsFinal: true, DetectedAt: 1},
	}}
	mgr := NewManager(primary, fallback)
	mgr.metrics = &Metrics{}

	for i := 1; i < fallbackTriggerLimit; i++ {
		_, err := mgr.Stream(context.Background(), "hello", VoiceAIFemalePro)
		if !IsHTTP5xx(err) {
			t.Fatalf("stream %d err = %v, want 5xx", i, err)
		}
		if mgr.UsingFallback() {
			t.Fatalf("should not switch before %d failures", fallbackTriggerLimit)
		}
	}

	ch, err := mgr.Stream(context.Background(), "hello", VoiceAIFemalePro)
	if err != nil {
		t.Fatalf("third stream should use fallback, err = %v", err)
	}
	if !mgr.UsingFallback() {
		t.Fatal("expected fallback switch")
	}
	got := collectChunks(t, ch)
	if len(got) != 1 || string(got[0].Data) != "duplex" {
		t.Fatalf("fallback chunks = %+v", got)
	}
	if primary.streams != fallbackTriggerLimit {
		t.Fatalf("primary streams = %d, want %d", primary.streams, fallbackTriggerLimit)
	}
	if fallback.streams != 1 {
		t.Fatalf("fallback streams = %d, want 1", fallback.streams)
	}
	if mgr.metrics.FallbackTriggered() != 1 {
		t.Fatalf("metric = %d, want 1", mgr.metrics.FallbackTriggered())
	}

	ch, err = mgr.Stream(context.Background(), "again", VoiceAIFemalePro)
	if err != nil {
		t.Fatalf("sticky fallback: %v", err)
	}
	_ = collectChunks(t, ch)
	if primary.streams != fallbackTriggerLimit {
		t.Fatalf("primary should stay unused after switch, streams = %d", primary.streams)
	}
	if fallback.streams != 2 {
		t.Fatalf("fallback streams = %d, want 2", fallback.streams)
	}
}

func TestManager_Non5xxDoesNotTripFallback(t *testing.T) {
	primary := &stubProvider{err: ErrEmptyText}
	fallback := &stubProvider{chunks: []AudioChunk{{Data: []byte("x"), Seq: 0, IsFinal: true, DetectedAt: 1}}}
	mgr := NewManager(primary, fallback)
	mgr.metrics = &Metrics{}
	for i := 0; i < 5; i++ {
		_, err := mgr.Stream(context.Background(), "hello", VoiceConfig{})
		if !errors.Is(err, ErrEmptyText) {
			t.Fatalf("err = %v", err)
		}
	}
	if mgr.UsingFallback() || mgr.metrics.FallbackTriggered() != 0 {
		t.Fatal("non-5xx must not trip fallback")
	}
}

func TestPrometheusMetrics_Format(t *testing.T) {
	body := PrometheusMetrics()
	for _, part := range []string{
		"tts_fallback_triggered_total",
		`from="volc_streaming"`,
		`to="volc_duplex"`,
		"# TYPE tts_fallback_triggered_total counter",
	} {
		if !strings.Contains(body, part) {
			t.Fatalf("missing %q in %q", part, body)
		}
	}
}
