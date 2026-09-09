package tts

import (
	"context"
	"encoding/base64"
	"errors"
	"io"
	"sync"
	"testing"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/voicepoc"
)

func TestDuplexFallback_Ping_Fail3(t *testing.T) {
	provider := &VolcDuplexFallbackProvider{
		ping: func(context.Context) error {
			return errors.New("dial refused")
		},
	}

	for i := 1; i < duplexPingFailLimit; i++ {
		if err := provider.Ping(context.Background()); err != nil {
			t.Fatalf("ping %d should swallow the failure, got %v", i, err)
		}
	}
	if err := provider.Ping(context.Background()); !errors.Is(err, ErrPingFailed) {
		t.Fatalf("ping %d err = %v, want ErrPingFailed", duplexPingFailLimit, err)
	}

	provider.ping = func(context.Context) error { return nil }
	if err := provider.Ping(context.Background()); err != nil {
		t.Fatalf("successful ping should reset: %v", err)
	}

	provider.ping = func(context.Context) error { return errors.New("dial refused") }
	if err := provider.Ping(context.Background()); err != nil {
		t.Fatalf("counter should reset after success, first new failure returned %v", err)
	}
}

func TestDuplexFallback_Stream_FromEvents(t *testing.T) {
	fixed := time.Date(2026, 9, 9, 21, 40, 0, 0, time.UTC)
	frame0 := base64.StdEncoding.EncodeToString([]byte("pcm-0"))
	frame1 := base64.StdEncoding.EncodeToString([]byte("pcm-1"))
	frame2 := base64.StdEncoding.EncodeToString([]byte("pcm-2"))

	script := &scriptedDuplex{
		batches: [][]duplexAudioEvent{
			{
				{Type: "response.output_audio.started"},
				{Type: "conversation.item.input_audio_transcription.completed", Delta: "ignore-me"},
				{Type: "response.output_audio.delta", Delta: frame0},
				{Type: "response.output_audio.delta", Delta: frame1},
				{Type: "response.output_audio.done"},
			},
			{
				{Type: "response.output_audio.delta", Delta: frame2},
				{Type: "response.done"},
			},
		},
	}

	provider := &VolcDuplexFallbackProvider{
		Config: voicepoc.DuplexConfig{Voice: defaultVolcTTSSpeaker},
		now:    func() time.Time { return fixed },
		open: func(context.Context) (duplexTTSConn, error) {
			script.opens++
			return script, nil
		},
	}

	first, err := provider.Stream(context.Background(), "hello fallback", VoiceConfig{VoiceID: "ignored"})
	if err != nil {
		t.Fatalf("Stream 1: %v", err)
	}
	got := collectChunks(t, first)
	if len(got) != 2 {
		t.Fatalf("first stream chunks = %+v", got)
	}
	if string(got[0].Data) != "pcm-0" || got[0].Seq != 0 || got[0].IsFinal || got[0].DetectedAt != fixed.UnixMilli() {
		t.Fatalf("chunk0 = %+v", got[0])
	}
	if string(got[1].Data) != "pcm-1" || got[1].Seq != 1 || !got[1].IsFinal {
		t.Fatalf("chunk1 = %+v", got[1])
	}

	second, err := provider.Stream(context.Background(), "second turn", VoiceConfig{})
	if err != nil {
		t.Fatalf("Stream 2: %v", err)
	}
	got = collectChunks(t, second)
	if len(got) != 1 || string(got[0].Data) != "pcm-2" || !got[0].IsFinal {
		t.Fatalf("second stream chunks = %+v", got)
	}

	if len(script.prompts) != 2 {
		t.Fatalf("RequestTTS calls = %d, want 2", len(script.prompts))
	}
	if script.opens != 1 {
		t.Fatalf("open calls = %d, want 1 (reuse session)", script.opens)
	}
	if err := provider.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if !script.closed {
		t.Fatal("expected reused duplex session to close")
	}
}

type scriptedDuplex struct {
	mu      sync.Mutex
	batches [][]duplexAudioEvent
	batch   int
	offset  int
	prompts []string
	opens   int
	closed  bool
}

func (s *scriptedDuplex) RequestTTS(_ context.Context, text string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.prompts = append(s.prompts, text)
	s.batch++
	s.offset = 0
	return nil
}

func (s *scriptedDuplex) Recv(ctx context.Context) (duplexAudioEvent, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if ctx.Err() != nil {
		return duplexAudioEvent{}, ctx.Err()
	}
	idx := s.batch - 1
	if idx < 0 || idx >= len(s.batches) {
		return duplexAudioEvent{}, io.EOF
	}
	events := s.batches[idx]
	if s.offset >= len(events) {
		return duplexAudioEvent{}, io.EOF
	}
	evt := events[s.offset]
	s.offset++
	return evt, nil
}

func (s *scriptedDuplex) Close(context.Context) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.closed = true
	return nil
}

func collectChunks(t *testing.T, ch <-chan AudioChunk) []AudioChunk {
	t.Helper()
	var got []AudioChunk
	for chunk := range ch {
		got = append(got, chunk)
	}
	return got
}
