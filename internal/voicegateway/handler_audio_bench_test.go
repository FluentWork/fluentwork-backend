package voicegateway

import (
	"context"
	"io"
	"log/slog"
	"testing"
)

func benchAudioFrameLogger(level slog.Level) *slog.Logger {
	return slog.New(slog.NewTextHandler(io.Discard, &slog.HandlerOptions{Level: level}))
}

func benchAudioFrameRuntime(tb testing.TB) (*sessionRuntime, []byte) {
	tb.Helper()
	session, err := MockVoiceProvider{}.Open(context.Background(), ConsumedTicket{}, nil, nil)
	if err != nil {
		tb.Fatalf("open mock provider: %v", err)
	}
	return &sessionRuntime{started: true, provider: session},
		make([]byte, ClientAudioFormat.FrameBytes(ClientFrameMS))
}

func BenchmarkHandleAudioFrame(b *testing.B) {
	benchmarkHandleAudioFrame(b, slog.LevelInfo)
}

func BenchmarkHandleAudioFrameDebugEmitted(b *testing.B) {
	benchmarkHandleAudioFrame(b, slog.LevelDebug)
}

func benchmarkHandleAudioFrame(b *testing.B, level slog.Level) {
	h := NewHandler(nil, nil, nil, benchAudioFrameLogger(level), Options{})
	rt, data := benchAudioFrameRuntime(b)
	session := ConsumedTicket{TicketID: "t1", SessionID: "s1", UserID: "u1"}
	ctx := context.Background()

	b.ReportAllocs()
	b.ResetTimer()
	for i := 0; i < b.N; i++ {
		if err := h.handleAudio(ctx, nil, data, rt, session); err != nil {
			b.Fatalf("handleAudio: %v", err)
		}
	}
}

func BenchmarkAudioFrameDebugCall(b *testing.B) {
	logger := benchAudioFrameLogger(slog.LevelInfo)
	ctx := context.Background()
	payloadBytes := ClientAudioFormat.FrameBytes(ClientFrameMS)

	b.Run("as_written", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			logger.Debug("received binary audio frame",
				"payload_bytes", payloadBytes,
				"session_started", true,
				"provider_nil", false,
			)
		}
	})

	b.Run("enabled_guarded", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			if logger.Enabled(ctx, slog.LevelDebug) {
				logger.Debug("received binary audio frame",
					"payload_bytes", payloadBytes,
					"session_started", true,
					"provider_nil", false,
				)
			}
		}
	})

	b.Run("one_field", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			logger.Debug("received binary audio frame", "payload_bytes", payloadBytes)
		}
	})

	b.Run("nothing", func(b *testing.B) {
		b.ReportAllocs()
		for i := 0; i < b.N; i++ {
			_ = payloadBytes
		}
	})
}
