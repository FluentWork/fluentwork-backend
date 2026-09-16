package voicegateway

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/conversation"
	"github.com/FluentWork/fluentwork-backend/internal/voiceproto"
)

// Rescue audio should sound like help, not like the AI taking its turn. These
// are the frozen product parameters (docs/78 §8.3): a slightly slower, slightly
// quieter voice than the conversation itself uses. They are declared here, next
// to the interface that will have to honour them, so the eventual synthesizer
// has one place to read them from rather than re-deriving them from prose.
const (
	RescueVoiceSpeed  = 0.9
	RescueVoiceVolume = 0.85
)

// RescueSynthesizer turns rescue text into audio the client can play.
//
// # Why this is an interface with no implementation
//
// The audio path is not built. B17's TTS package (internal/content/tts) carries
// its own "built, not turned on" warning, it lives in app-server rather than in
// the gateway process, and its internal endpoint answers base64 chunks rather
// than a URL the client could fetch — so there is no in-process way for the
// gateway to synthesize anything today. Rather than have the orchestrator
// fabricate a URL that iOS would then fail to fetch, it takes a synthesizer and
// runs with nil until one exists, emitting a text-only ladder in the meantime.
//
// A nil synthesizer is therefore a supported configuration, not an error: the
// user still gets the prompt, and the client decides whether to speak it itself.
type RescueSynthesizer interface {
	// Synthesize returns a URL the client can play and the audio's duration.
	// Implementations must use RescueVoiceSpeed / RescueVoiceVolume.
	Synthesize(ctx context.Context, text, turnID string) (audioURL string, durationMS int64, err error)
}

// RescueOrchestrator coordinates rescue ladder generation and TTS synthesis.
type RescueOrchestrator struct {
	rescueGen   RescueGenerator
	synth       RescueSynthesizer
	logger      *slog.Logger
	fallbackLib map[int][]string // Fallback text library when LLM fails
}

// RescueGenerator interface for generating rescue text (allows mocking in tests)
type RescueGenerator interface {
	GenerateRescue(ctx context.Context, level conversation.RescueLevel, conv conversation.ConversationContext) (string, error)
}

// NewRescueOrchestrator creates a new rescue orchestrator. synth may be nil,
// which yields text-only ladders (see RescueSynthesizer).
func NewRescueOrchestrator(
	rescueGen RescueGenerator,
	synth RescueSynthesizer,
	logger *slog.Logger,
) *RescueOrchestrator {
	if logger == nil {
		logger = slog.Default()
	}

	return &RescueOrchestrator{
		rescueGen:   rescueGen,
		synth:       synth,
		logger:      logger.With("component", "rescue_orchestrator"),
		fallbackLib: buildFallbackLibrary(),
	}
}

// GenerateAndSynthesize generates rescue text and, when a synthesizer is
// wired, synthesizes it. Returns a RescueLadder frame ready to send.
//
// Both halves degrade rather than fail. Text generation falls back to a fixed
// library (docs/78 §8.1); synthesis failure leaves AudioURL empty. In neither
// case does the ladder go unsent — a prompt the user can read beats no prompt,
// and the text is the payload that makes the feature work.
func (o *RescueOrchestrator) GenerateAndSynthesize(
	ctx context.Context,
	level int,
	turnID string,
	convCtx conversation.ConversationContext,
) (*voiceproto.RescueLadder, error) {
	if !voiceproto.ValidRescueLevel(level) {
		return nil, fmt.Errorf("invalid rescue level: %d", level)
	}

	text, err := o.generateText(ctx, level, convCtx)
	if err != nil {
		o.logger.Warn("rescue text generation failed, using fallback",
			"level", level,
			"turn_id", turnID,
			"err", err,
		)
		text = o.getFallback(level)
	}

	frame := &voiceproto.RescueLadder{
		Type:   voiceproto.TypeRescueLadder,
		TurnID: turnID,
		Level:  level,
		Text:   text,
		TS:     time.Now().UnixMilli(),
	}

	if o.synth == nil {
		o.logger.Warn("B8 rescue audio unavailable; emitting text-only ladder",
			"level", level,
			"turn_id", turnID,
			"stage", "b8_rescue",
		)
		return frame, nil
	}

	audioURL, durationMS, err := o.synth.Synthesize(ctx, text, turnID)
	if err != nil {
		o.logger.Warn("B8 rescue TTS synthesis failed; emitting text-only ladder",
			"level", level,
			"turn_id", turnID,
			"err", err,
			"stage", "b8_rescue",
		)
		return frame, nil
	}

	frame.AudioURL = audioURL
	frame.DurationMS = durationMS
	return frame, nil
}

func (o *RescueOrchestrator) generateText(
	ctx context.Context,
	level int,
	convCtx conversation.ConversationContext,
) (string, error) {
	var rescueLevel conversation.RescueLevel
	switch level {
	case voiceproto.RescueLevelSkeleton:
		rescueLevel = conversation.RescueSkeleton
	case voiceproto.RescueLevelHint:
		rescueLevel = conversation.RescueHint
	case voiceproto.RescueLevelComplete:
		rescueLevel = conversation.RescueComplete
	default:
		return "", fmt.Errorf("invalid level: %d", level)
	}

	// Bound the generation so a slow model cannot hold the ladder past the next
	// rung's threshold. The budget is the rung spacing (3s) rather than a
	// comfortable round number: a rescue that lands after the next one is due
	// has already lost the race it was generated for.
	genCtx, cancel := context.WithTimeout(ctx, DefaultRescueLevel1After)
	defer cancel()

	return o.rescueGen.GenerateRescue(genCtx, rescueLevel, convCtx)
}

func (o *RescueOrchestrator) getFallback(level int) string {
	texts, ok := o.fallbackLib[level]
	if !ok || len(texts) == 0 {
		return "I think..." // Ultimate fallback
	}
	// Return first item for now; can randomize in future
	return texts[0]
}

func buildFallbackLibrary() map[int][]string {
	return map[int][]string{
		voiceproto.RescueLevelSkeleton: {
			"I think...",
			"The key point is...",
			"In my view...",
			"From my perspective...",
		},
		voiceproto.RescueLevelHint: {
			"先说结论",
			"举个例子",
			"分几点说",
			"用因果关系",
		},
		voiceproto.RescueLevelComplete: {
			"I think we should consider the trade-offs carefully.",
			"The main benefit is that it improves efficiency.",
			"One concern I have is the maintenance cost.",
		},
	}
}
