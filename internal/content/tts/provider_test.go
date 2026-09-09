package tts

import (
	"context"
	"errors"
	"testing"
	"time"
)

type mockProvider struct {
	chunks []AudioChunk
	closed bool
}

func (m *mockProvider) Stream(ctx context.Context, text string, voice VoiceConfig) (<-chan AudioChunk, error) {
	if m.closed {
		return nil, ErrClosed
	}
	if _, _, err := NormalizeStreamInput(text, voice); err != nil {
		return nil, err
	}
	out := make(chan AudioChunk)
	go func() {
		defer close(out)
		for _, chunk := range m.chunks {
			select {
			case <-ctx.Done():
				return
			case out <- chunk:
			}
		}
	}()
	return out, nil
}

func (m *mockProvider) Ping(context.Context) error {
	if m.closed {
		return ErrClosed
	}
	return nil
}

func (m *mockProvider) Close() error {
	m.closed = true
	return nil
}

var _ Provider = (*mockProvider)(nil)

func TestProvider_Interface_Contract(t *testing.T) {
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC).UnixMilli()
	provider := &mockProvider{
		chunks: []AudioChunk{
			{Data: []byte("frame-0"), Seq: 0, DetectedAt: now},
			{Data: []byte("frame-1"), Seq: 1, IsFinal: true, DetectedAt: now},
		},
	}

	if err := provider.Ping(context.Background()); err != nil {
		t.Fatalf("Ping: %v", err)
	}

	if _, err := provider.Stream(context.Background(), "   ", VoiceConfig{}); !errors.Is(err, ErrEmptyText) {
		t.Fatalf("empty text err = %v, want ErrEmptyText", err)
	}

	ch, err := provider.Stream(context.Background(), "hello", VoiceConfig{VoiceID: "zh_female_vv_jupiter_bigtts"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	var got []AudioChunk
	for chunk := range ch {
		if err := chunk.Validate(); err != nil {
			t.Fatalf("chunk %d: %v", chunk.Seq, err)
		}
		got = append(got, chunk)
	}
	if len(got) != 2 {
		t.Fatalf("got %d chunks, want 2", len(got))
	}
	if got[0].Seq != 0 || got[1].Seq != 1 || !got[1].IsFinal || got[0].IsFinal {
		t.Fatalf("unexpected stream: %+v", got)
	}

	cancelCtx, cancel := context.WithCancel(context.Background())
	cancel()
	cancelled, err := provider.Stream(cancelCtx, "hello", VoiceConfig{})
	if err != nil {
		t.Fatalf("cancelled Stream: %v", err)
	}
	if _, ok := <-cancelled; ok {
		t.Fatal("expected cancelled stream to close without chunks")
	}

	if err := provider.Close(); err != nil {
		t.Fatalf("Close: %v", err)
	}
	if err := provider.Ping(context.Background()); !errors.Is(err, ErrClosed) {
		t.Fatalf("Ping after Close err = %v, want ErrClosed", err)
	}
	if _, err := provider.Stream(context.Background(), "hello", VoiceConfig{}); !errors.Is(err, ErrClosed) {
		t.Fatalf("Stream after Close err = %v, want ErrClosed", err)
	}
}

func TestAudioChunk_Schema(t *testing.T) {
	ok := AudioChunk{Data: []byte{0x4f, 0x70}, Seq: 0, DetectedAt: 1}
	if err := ok.Validate(); err != nil {
		t.Fatalf("valid chunk: %v", err)
	}

	cases := []struct {
		name  string
		chunk AudioChunk
	}{
		{name: "empty data", chunk: AudioChunk{Seq: 0, DetectedAt: 1}},
		{name: "negative seq", chunk: AudioChunk{Data: []byte{1}, Seq: -1, DetectedAt: 1}},
		{name: "negative detected_at", chunk: AudioChunk{Data: []byte{1}, Seq: 0, DetectedAt: -1}},
	}
	for _, tc := range cases {
		if err := tc.chunk.Validate(); !errors.Is(err, ErrInvalidChunk) {
			t.Fatalf("%s: err = %v, want ErrInvalidChunk", tc.name, err)
		}
	}

	final := AudioChunk{Data: []byte("last"), Seq: 9, IsFinal: true, DetectedAt: 1}
	if err := final.Validate(); err != nil {
		t.Fatalf("final chunk should still carry payload: %v", err)
	}
}

func TestVoiceConfig_Default(t *testing.T) {
	got := VoiceConfig{VoiceID: "en_female_professional"}.WithDefaults()
	if got.VoiceID != "en_female_professional" {
		t.Fatalf("VoiceID = %q", got.VoiceID)
	}
	if got.Speed != DefaultSpeed {
		t.Fatalf("Speed = %v, want %v", got.Speed, DefaultSpeed)
	}

	custom := VoiceConfig{VoiceID: "en_male_narrator", Speed: 0.85}.WithDefaults()
	if custom.Speed != 0.85 {
		t.Fatalf("custom Speed = %v, want 0.85", custom.Speed)
	}

	text, voice, err := NormalizeStreamInput("  ship it  ", VoiceConfig{})
	if err != nil {
		t.Fatalf("NormalizeStreamInput: %v", err)
	}
	if text != "ship it" {
		t.Fatalf("text = %q", text)
	}
	if voice.Speed != DefaultSpeed {
		t.Fatalf("normalized Speed = %v, want %v", voice.Speed, DefaultSpeed)
	}

	if _, _, err := NormalizeStreamInput("\n\t", VoiceConfig{}); !errors.Is(err, ErrEmptyText) {
		t.Fatalf("blank text err = %v, want ErrEmptyText", err)
	}
}
