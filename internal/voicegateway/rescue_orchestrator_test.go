package voicegateway

import (
	"context"
	"errors"
	"log/slog"
	"slices"
	"testing"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/conversation"
	"github.com/FluentWork/fluentwork-backend/internal/voiceproto"
)

// mockRescueGenerator for testing
type mockRescueGenerator struct {
	generateFunc func(ctx context.Context, level conversation.RescueLevel, conv conversation.ConversationContext) (string, error)
}

func (m *mockRescueGenerator) GenerateRescue(
	ctx context.Context,
	level conversation.RescueLevel,
	conv conversation.ConversationContext,
) (string, error) {
	if m.generateFunc != nil {
		return m.generateFunc(ctx, level, conv)
	}
	return "mock rescue text", nil
}

// fakeSynthesizer records what it was asked to synthesize. Returns a fixed
// amount of 16 kHz PCM when pcm/err are unset, so a test can exercise the
// success path without spelling out the bytes twice.
type fakeSynthesizer struct {
	calls    int
	lastText string
	lastTurn string
	pcm      []byte
	err      error
}

func (f *fakeSynthesizer) Synthesize(_ context.Context, text, turnID string) (RescueAudio, error) {
	f.calls++
	f.lastText = text
	f.lastTurn = turnID
	if f.err != nil {
		return RescueAudio{}, f.err
	}
	pcm := f.pcm
	if pcm == nil {
		pcm = make([]byte, 3200*3) // 300 ms of 16 kHz mono s16le
	}
	return RescueAudio{PCM: pcm, SampleRate: 16000, Codec: "pcm", VoiceID: "test-voice"}, nil
}

// **发帧排在合成之前** —— A 的契约，用调用**顺序**钉住，而不是延迟。
//
// 为什么不看延迟：合成快的时候先发后发看起来一模一样（2026-09-30 真机三层里两层
// 正常，唯一丢声的那层才暴露出来）。顺序是确定的，延迟不是。
func TestRescueOrchestrator_TextIsDeliveredBeforeSynthesis(t *testing.T) {
	var order []string

	gen := &mockRescueGenerator{
		generateFunc: func(context.Context, conversation.RescueLevel, conversation.ConversationContext) (string, error) {
			order = append(order, "generate")
			return "I think the main risk is...", nil
		},
	}
	synth := &orderRecordingSynthesizer{order: &order}
	orch := NewRescueOrchestrator(gen, synth, 0, slog.New(slog.DiscardHandler))

	delivery, err := orch.GenerateAndSynthesize(
		context.Background(), 1, "t_order", conversation.ConversationContext{},
		func(frame *voiceproto.RescueLadder) error {
			if frame.Text == "" {
				t.Error("deliverText got an empty text")
			}
			order = append(order, "deliver")
			return nil
		},
	)
	if err != nil {
		t.Fatalf("GenerateAndSynthesize failed: %v", err)
	}
	if delivery.Audio == nil {
		t.Fatal("no audio on the delivery")
	}
	if want := []string{"generate", "deliver", "synth"}; !slices.Equal(order, want) {
		t.Fatalf("order = %v, want %v — 发帧必须排在合成之前（PRD §5.4 硬约束 #2：骨架与提示走 TTS 说出来）", order, want)
	}

	// 发帧失败 ⇒ **不许**合成：提示没落地，就没有要说的东西。
	boom := errors.New("wire is gone")
	failing := &orderRecordingSynthesizer{order: &order}
	failingOrch := NewRescueOrchestrator(gen, failing, 0, slog.New(slog.DiscardHandler))
	if _, err := failingOrch.GenerateAndSynthesize(
		context.Background(), 1, "t_order_fail", conversation.ConversationContext{},
		func(*voiceproto.RescueLadder) error { return boom },
	); !errors.Is(err, boom) {
		t.Errorf("err = %v, want %v", err, boom)
	}
	if failing.calls != 0 {
		t.Errorf("synthesizer ran %d times after the prompt failed to land, want 0", failing.calls)
	}
}

// orderRecordingSynthesizer appends to a shared order slice so a test can assert
// where synthesis sat relative to the other two steps.
type orderRecordingSynthesizer struct {
	order *[]string
	calls int
}

func (s *orderRecordingSynthesizer) Synthesize(context.Context, string, string) (RescueAudio, error) {
	s.calls++
	*s.order = append(*s.order, "synth")
	return RescueAudio{PCM: []byte{1, 2, 3, 4}, SampleRate: 16000, Codec: "pcm"}, nil
}

func TestRescueOrchestrator_GenerateAndSynthesize_Level1(t *testing.T) {
	mockGen := &mockRescueGenerator{
		generateFunc: func(_ context.Context, level conversation.RescueLevel, _ conversation.ConversationContext) (string, error) {
			if level != conversation.RescueSkeleton {
				t.Errorf("Expected RescueSkeleton, got %v", level)
			}
			return "I think the main risk is...", nil
		},
	}
	synth := &fakeSynthesizer{}

	orch := NewRescueOrchestrator(mockGen, synth, 0, nil)

	convCtx := conversation.ConversationContext{
		LastAIMessage:   "What's the biggest risk?",
		ScenarioContext: "System design discussion",
		UserRole:        "Backend Engineer",
	}

	delivery, err := orch.GenerateAndSynthesize(context.Background(), 1, "t_123", convCtx, nil)
	if err != nil {
		t.Fatalf("GenerateAndSynthesize failed: %v", err)
	}
	frame := delivery.Frame

	if frame.Type != voiceproto.TypeRescueLadder {
		t.Errorf("Expected type=%s, got %s", voiceproto.TypeRescueLadder, frame.Type)
	}
	if frame.Level != 1 {
		t.Errorf("Expected level=1, got %d", frame.Level)
	}
	if frame.TurnID != "t_123" {
		t.Errorf("Expected turn_id=t_123, got %s", frame.TurnID)
	}
	if frame.Text != "I think the main risk is..." {
		t.Errorf("Expected generated skeleton, got %s", frame.Text)
	}
	// Audio rides the ai.tts.* stream, not this frame — the frame's own fields
	// stay empty so the client is not told to fetch something nobody serves.
	if frame.AudioURL != "" || frame.DurationMS != 0 {
		t.Errorf("ladder frame must not carry audio metadata: url=%q duration=%d", frame.AudioURL, frame.DurationMS)
	}
	if delivery.Audio == nil {
		t.Fatal("Expected synthesized audio on the delivery")
	}
	// The rung's length is not asserted here any more: how long it takes to
	// speak is what the *writer* reports, and it reports what it actually sent
	// rather than what was synthesized. That assertion lives with the writer.
	if got := delivery.Audio.SampleRate; got != 16000 {
		t.Errorf("sample rate = %d, want 16000", got)
	}
	if frame.TS <= 0 {
		t.Error("Expected a non-zero ts")
	}
	// The synthesizer must be handed the rescue text and the turn, not the
	// prompt or the session — it has no other way to know either.
	if synth.lastText != frame.Text || synth.lastTurn != "t_123" {
		t.Errorf("synthesizer got (%q, %q), want (%q, %q)",
			synth.lastText, synth.lastTurn, frame.Text, "t_123")
	}
}

func TestRescueOrchestrator_GenerateAndSynthesize_Level2(t *testing.T) {
	mockGen := &mockRescueGenerator{
		generateFunc: func(_ context.Context, level conversation.RescueLevel, _ conversation.ConversationContext) (string, error) {
			if level != conversation.RescueHint {
				t.Errorf("Expected RescueHint, got %v", level)
			}
			return "先说结论，再说原因", nil
		},
	}

	orch := NewRescueOrchestrator(mockGen, &fakeSynthesizer{}, 0, nil)

	delivery, err := orch.GenerateAndSynthesize(context.Background(), 2, "t_456", conversation.ConversationContext{
		LastAIMessage: "Why do you prefer this approach?",
	}, nil)
	if err != nil {
		t.Fatalf("GenerateAndSynthesize failed: %v", err)
	}
	frame := delivery.Frame

	if frame.Level != 2 {
		t.Errorf("Expected level=2, got %d", frame.Level)
	}
	if frame.Text != "先说结论，再说原因" {
		t.Errorf("Expected Chinese hint, got %s", frame.Text)
	}
}

func TestRescueOrchestrator_GenerateAndSynthesize_Level3(t *testing.T) {
	mockGen := &mockRescueGenerator{
		generateFunc: func(_ context.Context, level conversation.RescueLevel, _ conversation.ConversationContext) (string, error) {
			if level != conversation.RescueComplete {
				t.Errorf("Expected RescueComplete, got %v", level)
			}
			return "I think we should use a blue-green deployment strategy.", nil
		},
	}

	orch := NewRescueOrchestrator(mockGen, &fakeSynthesizer{}, 0, nil)

	delivery, err := orch.GenerateAndSynthesize(context.Background(), 3, "t_789", conversation.ConversationContext{
		LastAIMessage: "How should we handle the deployment?",
	}, nil)
	if err != nil {
		t.Fatalf("GenerateAndSynthesize failed: %v", err)
	}
	frame := delivery.Frame

	if frame.Level != 3 {
		t.Errorf("Expected level=3, got %d", frame.Level)
	}
	if frame.Text == "" {
		t.Error("Expected non-empty complete response")
	}
}

func TestRescueOrchestrator_InvalidLevel(t *testing.T) {
	orch := NewRescueOrchestrator(&mockRescueGenerator{}, &fakeSynthesizer{}, 0, nil)

	for _, level := range []int{-1, 0, 4, 99} {
		if _, err := orch.GenerateAndSynthesize(context.Background(), level, "t_invalid", conversation.ConversationContext{}, nil); err == nil {
			t.Errorf("Expected error for invalid level %d, got nil", level)
		}
	}
}

func TestRescueOrchestrator_FallbackOnGenerationError(t *testing.T) {
	mockGen := &mockRescueGenerator{
		generateFunc: func(_ context.Context, _ conversation.RescueLevel, _ conversation.ConversationContext) (string, error) {
			return "", context.DeadlineExceeded
		},
	}

	orch := NewRescueOrchestrator(mockGen, &fakeSynthesizer{}, 0, nil)

	delivery, err := orch.GenerateAndSynthesize(context.Background(), 1, "t_fallback", conversation.ConversationContext{}, nil)
	if err != nil {
		t.Fatalf("Expected fallback to succeed, got error: %v", err)
	}
	frame := delivery.Frame

	// The fallback library must actually be used, not an empty string: a ladder
	// with no text is a frame the client cannot show.
	if frame.Text != "I think..." {
		t.Errorf("Expected fallback text %q, got %q", "I think...", frame.Text)
	}
}

func TestRescueOrchestrator_GetFallback(t *testing.T) {
	orch := NewRescueOrchestrator(&mockRescueGenerator{}, &fakeSynthesizer{}, 0, nil)

	tests := []struct {
		level    int
		expected string
	}{
		{voiceproto.RescueLevelSkeleton, "I think..."},
		{voiceproto.RescueLevelHint, "先说结论"},
		{voiceproto.RescueLevelComplete, "I think we should consider the trade-offs carefully."},
	}

	for _, tt := range tests {
		if result := orch.getFallback(tt.level); result != tt.expected {
			t.Errorf("Level %d: expected %s, got %s", tt.level, tt.expected, result)
		}
	}
}

// A gateway with no synthesizer still delivers the prompt. This is the
// configuration production runs in while B17's TTS stays off, so it is a
// supported path rather than a degraded one — and it must not invent an audio
// URL, which would send iOS to fetch something that does not exist.
func TestRescueOrchestrator_NilSynthesizerEmitsTextOnlyLadder(t *testing.T) {
	orch := NewRescueOrchestrator(&mockRescueGenerator{
		generateFunc: func(_ context.Context, _ conversation.RescueLevel, _ conversation.ConversationContext) (string, error) {
			return "The key point is...", nil
		},
	}, nil, 0, nil)

	delivery, err := orch.GenerateAndSynthesize(context.Background(), 1, "t_textonly", conversation.ConversationContext{}, nil)
	if err != nil {
		t.Fatalf("Expected text-only ladder to succeed, got error: %v", err)
	}
	frame := delivery.Frame
	if frame.Text != "The key point is..." {
		t.Errorf("Expected generated text, got %q", frame.Text)
	}
	if frame.AudioURL != "" {
		t.Errorf("Expected empty audio_url without a synthesizer, got %q", frame.AudioURL)
	}
	if frame.DurationMS != 0 {
		t.Errorf("Expected zero duration_ms without audio, got %d", frame.DurationMS)
	}
}

// Synthesis failure must not swallow the rung. The text is the part that makes
// the feature work; the audio is an enhancement on top of it.
func TestRescueOrchestrator_SynthesisFailureStillEmitsLadder(t *testing.T) {
	synth := &fakeSynthesizer{err: errors.New("tts unavailable")}

	orch := NewRescueOrchestrator(&mockRescueGenerator{
		generateFunc: func(_ context.Context, _ conversation.RescueLevel, _ conversation.ConversationContext) (string, error) {
			return "先说结论", nil
		},
	}, synth, 0, nil)

	delivery, err := orch.GenerateAndSynthesize(context.Background(), 2, "t_synthfail", conversation.ConversationContext{}, nil)
	if err != nil {
		t.Fatalf("Expected ladder despite synthesis failure, got error: %v", err)
	}
	frame := delivery.Frame
	if frame.Text != "先说结论" {
		t.Errorf("Expected hint text preserved, got %q", frame.Text)
	}
	if frame.AudioURL != "" {
		t.Errorf("Expected empty audio_url after synthesis failure, got %q", frame.AudioURL)
	}
	if frame.Level != 2 {
		t.Errorf("Expected level=2, got %d", frame.Level)
	}
}

// The generation deadline has to be shorter than the gap between two rungs.
// A ladder that lands after the next one is due has already lost the race it was
// generated for, so this pins the relationship rather than the literal number.
func TestRescueOrchestrator_GenerationDeadlineIsInsideRungSpacing(t *testing.T) {
	orch := NewRescueOrchestrator(&mockRescueGenerator{
		generateFunc: func(ctx context.Context, _ conversation.RescueLevel, _ conversation.ConversationContext) (string, error) {
			deadline, ok := ctx.Deadline()
			if !ok {
				t.Error("Expected the generator to run under a deadline")
				return "x", nil
			}
			if remaining := time.Until(deadline); remaining > DefaultRescueLevel1After {
				t.Errorf("Generation deadline %v exceeds rung spacing %v", remaining, DefaultRescueLevel1After)
			}
			return "I think...", nil
		},
	}, &fakeSynthesizer{}, 0, nil)

	if _, err := orch.GenerateAndSynthesize(context.Background(), 1, "t_deadline", conversation.ConversationContext{}, nil); err != nil {
		t.Fatalf("GenerateAndSynthesize failed: %v", err)
	}
}
