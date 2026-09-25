package voicegateway

import (
	"context"
	"fmt"
	"log/slog"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/conversation"
	"github.com/FluentWork/fluentwork-backend/internal/voiceproto"
)

// Rescue audio should sound like help, not like the AI taking its turn. These are
// the frozen product parameters (docs/78 §8.3): a slightly slower, slightly
// quieter voice than the conversation itself uses.
//
// They are stated here as the product's numbers, but neither is applied in this
// package — and that split is deliberate (docs/92 §5):
//
//   - **Speed** is a synthesis parameter, so it lives in app-server's voice
//     catalog as tts.VoiceRescueLadder, which the gateway asks for by name
//     ("rescue_ladder"). Slowing PCM down after it is synthesized is not a thing
//     a byte stream can do.
//   - **Volume** is applied at playback. The ladder arrives as PCM with no gain
//     applied, and the client is where a gain can be applied without resampling
//     it into the conversation's stream.
//
// Kept as constants because the numbers are the product's decision and both
// sides need to agree on them; the synthesizer that reads them is elsewhere.
const (
	RescueVoiceSpeed  = 0.9
	RescueVoiceVolume = 0.85
)

// RescueAudio is a synthesized rung the gateway can put on the wire.
//
// It is bytes rather than a URL because of how the client plays audio: binary
// frames are only played between an ai.tts.start and an ai.tts.end, and the
// player's buffer is 16 kHz mono PCM16. A URL would mean inventing a fetch path,
// a store to fetch from and a decoder for whatever format came out of it; the
// bytes the vendor already returns are what the client already plays (docs/92).
type RescueAudio struct {
	PCM        []byte
	SampleRate int
	Codec      string
	VoiceID    string
}

// RescueSynthesizer turns rescue text into audio.
//
// A nil synthesizer is a supported configuration, not an error: the ladder goes
// out as text and the client decides whether to speak it itself. The same holds
// when synthesis fails — text is the payload that makes the feature work, audio
// is how it lands.
type RescueSynthesizer interface {
	// Synthesize speaks one rung. Implementations must use the rescue voice
	// (slower than the conversation — see tts.VoiceRescueLadder).
	Synthesize(ctx context.Context, text, turnID string) (RescueAudio, error)
}

// RescueDelivery is one rung ready to send: the ladder frame plus, when it could
// be synthesized, the audio that speaks it.
type RescueDelivery struct {
	Frame *voiceproto.RescueLadder
	Audio *RescueAudio
}

// RescueOrchestrator coordinates rescue ladder generation and TTS synthesis.
type RescueOrchestrator struct {
	rescueGen   RescueGenerator
	synth       RescueSynthesizer
	logger      *slog.Logger
	fallbackLib map[int][]string // Fallback text library when LLM fails
	// rungBudget bounds text generation. It is the ladder's first rung spacing
	// rather than a comfortable round number: a rescue that lands after the next
	// one is due has already lost the race it was generated for.
	rungBudget time.Duration
}

// RescueGenerator interface for generating rescue text (allows mocking in tests)
type RescueGenerator interface {
	GenerateRescue(ctx context.Context, level conversation.RescueLevel, conv conversation.ConversationContext) (string, error)
}

// NewRescueOrchestrator creates a new rescue orchestrator. synth may be nil,
// which yields text-only ladders (see RescueSynthesizer). A rungBudget of zero
// or less uses DefaultRescueLevel1After.
func NewRescueOrchestrator(
	rescueGen RescueGenerator,
	synth RescueSynthesizer,
	rungBudget time.Duration,
	logger *slog.Logger,
) *RescueOrchestrator {
	if logger == nil {
		logger = slog.Default()
	}
	if rungBudget <= 0 {
		rungBudget = DefaultRescueLevel1After
	}

	return &RescueOrchestrator{
		rescueGen:   rescueGen,
		synth:       synth,
		logger:      logger.With("component", "rescue_orchestrator"),
		fallbackLib: buildFallbackLibrary(),
		rungBudget:  rungBudget,
	}
}

// GenerateAndSynthesize generates rescue text and, when a synthesizer is wired,
// speaks it. Both halves degrade rather than fail: text generation falls back to
// a fixed library (docs/78 §8.1), and a failed synthesis returns the frame with
// no audio. In neither case does the ladder go unsent — a prompt the user can
// read beats no prompt, and the text is the payload that makes the feature work.
//
// Audio never delays text. The caller sends the frame first and the audio after
// (see handler_rescue.emitRescue): waiting for a model to speak before showing
// the learner what to say would spend the rung's whole budget on the half that
// is optional.
func (o *RescueOrchestrator) GenerateAndSynthesize(
	ctx context.Context,
	level int,
	turnID string,
	convCtx conversation.ConversationContext,
) (RescueDelivery, error) {
	if !voiceproto.ValidRescueLevel(level) {
		return RescueDelivery{}, fmt.Errorf("invalid rescue level: %d", level)
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
	delivery := RescueDelivery{Frame: frame}

	if o.synth == nil {
		o.logger.Warn("B8 rescue audio unavailable; emitting text-only ladder",
			"level", level,
			"turn_id", turnID,
			"stage", "b8_rescue",
		)
		return delivery, nil
	}

	audio, err := o.synth.Synthesize(ctx, text, turnID)
	if err != nil {
		o.logger.Warn("B8 rescue TTS synthesis failed; emitting text-only ladder",
			"level", level,
			"turn_id", turnID,
			"err", err,
			"stage", "b8_rescue",
		)
		return delivery, nil
	}
	if len(audio.PCM) == 0 {
		// A 200 with no bytes is a failure that looks like success; treat it as
		// one rather than sending an ai.tts.start the client can never finish.
		o.logger.Warn("B8 rescue TTS returned no audio; emitting text-only ladder",
			"level", level,
			"turn_id", turnID,
			"stage", "b8_rescue",
		)
		return delivery, nil
	}
	delivery.Audio = &audio
	return delivery, nil
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
	// rung's threshold. The budget is the rung spacing rather than a comfortable
	// round number: a rescue that lands after the next one is due has already
	// lost the race it was generated for.
	//
	// It follows the configured spacing (VOICE_RESCUE_LEVEL1_AFTER) rather than
	// the compiled-in default: shortening the rungs while leaving this at 3s
	// would let a rung outlive the next one's due time, which is the failure this
	// bound exists to prevent.
	genCtx, cancel := context.WithTimeout(ctx, o.rungBudget)
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
