package voicegateway

import (
	"context"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/FluentWork/fluentwork-backend/internal/voiceproto"
)

// DevEchoVoiceProvider is a dev-only provider that simulates a server-side
// ASR relay without standing up a real voice stack.
//
// Why this exists: B14 changed the contract so `user.speech.end` from iOS
// has `text: nil` by default — the authoritative ASR text is supposed to
// come from the provider via ProviderOutbound.ServerASRText. The default
// `MockVoiceProvider` returns empty ServerASRText, which means even with
// the corpus seeded and the B12 BadgeEmitter wired, no badge fires
// because the detector has no input text.
//
// DevEchoVoiceProvider fills that gap locally: it ignores the audio stream
// and on every `user.speech.end` returns the configured text as
// `ServerASRText` + a `ClientASRTranscription` control frame. cmd/voice-gateway
// wires a self-contained B12 emitter for this provider, so the badge fires
// deterministically and the iOS overlay shows it. No Volcengine credentials
// needed.
//
// This provider MUST NOT be selectable in production builds — keep it
// behind `VOICE_GATEWAY_PROVIDER=dev-echo` and document that fact in
// the env example. The provider name is intentionally verbose so it
// can't be confused with a real provider in incident response.
type DevEchoVoiceProvider struct {
	EchoText string
	Logger   *slog.Logger
}

// NewDevEchoVoiceProvider reads its configuration from struct fields.
// Callers should populate EchoText from VOICE_DEV_ECHO_TEXT (env-driven)
// or from a test fixture.
func NewDevEchoVoiceProvider(echoText string, logger *slog.Logger) DevEchoVoiceProvider {
	if logger == nil {
		logger = slog.Default()
	}
	return DevEchoVoiceProvider{
		EchoText: strings.TrimSpace(echoText),
		Logger:   logger.With("component", "voicegateway.dev_echo_provider"),
	}
}

// Open returns a fresh session that echoes the configured text.
func (p DevEchoVoiceProvider) Open(_ context.Context, ticket ConsumedTicket) (VoiceProviderSession, error) {
	if strings.TrimSpace(p.EchoText) == "" {
		p.Logger.Warn("dev-echo provider started with empty EchoText; badges will never fire",
			"session_id", ticket.SessionID,
			"hint", "set VOICE_DEV_ECHO_TEXT or use MockVoiceProvider",
		)
	}
	return &devEchoSession{
		echoText: p.EchoText,
		nextSeq:  1,
	}, nil
}

type devEchoSession struct {
	echoText string
	nextSeq  int
}

// Start emits a placeholder AI greeting so iOS sees a normal session
// boot sequence (mirrors MockVoiceProvider.Start).
func (s *devEchoSession) Start(_ context.Context, _ voiceproto.SessionStart) ([]ProviderOutbound, error) {
	const stub = "ready"
	return []ProviderOutbound{
		{
			Control: map[string]any{
				"type":    voiceproto.TypeAITextDelta,
				"text":    stub,
				"turn_id": "bootstrap",
			},
		},
		{
			Control: voiceproto.AITurnEnd{
				Type:   voiceproto.TypeAITurnEnd,
				TurnID: "bootstrap",
			},
		},
	}, nil
}

// HandleClientControl echoes back the configured ServerASRText on
// user.speech.end so the gateway has authoritative text for B12 hit
// detection. It always closes the turn with ai.turn.end so the client's
// speech state machine can leave `.processing` even when echo text is empty.
// Audio frames are not consulted — the dev echo provider deliberately does
// not transcribe.
func (s *devEchoSession) HandleClientControl(_ context.Context, frameType string, data []byte) ([]ProviderOutbound, error) {
	if frameType != voiceproto.TypeUserSpeechEnd {
		return nil, nil
	}
	turnID := "dev-echo-turn"
	var end voiceproto.UserSpeechEnd
	if err := json.Unmarshal(data, &end); err == nil && strings.TrimSpace(end.TurnID) != "" {
		turnID = strings.TrimSpace(end.TurnID)
	}
	echoText := strings.TrimSpace(s.echoText)
	if echoText == "" {
		// No authoritative transcript → no relay frame and no badge, but the
		// turn must still end so iOS does not hang in `.processing`.
		return []ProviderOutbound{
			{
				Control: voiceproto.AITurnEnd{
					Type:   voiceproto.TypeAITurnEnd,
					TurnID: turnID,
				},
			},
		}, nil
	}
	return []ProviderOutbound{
		{
			Control: voiceproto.ClientASRTranscription{
				Type:   voiceproto.TypeClientASRTranscription,
				Text:   echoText,
				TurnID: turnID,
			},
			// B14: this is the authoritative text the gateway will feed
			// into the B12 hit detector when the client sends `text: nil`.
			ServerASRText: echoText,
		},
		{
			// Explicit assistant-turn boundary so the client state machine
			// transitions `.processing → .waitingUser`.
			Control: voiceproto.AITurnEnd{
				Type:   voiceproto.TypeAITurnEnd,
				TurnID: turnID,
			},
		},
	}, nil
}

// HandleClientAudio is a no-op for dev-echo: the provider never transcribes
// audio itself, so binary frames carry no outbound state.
func (s *devEchoSession) HandleClientAudio(_ context.Context, _ []byte) ([]ProviderOutbound, error) {
	return nil, nil
}

// SnapshotUtterances is empty — dev sessions do not build a real
// utterance timeline.
func (s *devEchoSession) SnapshotUtterances() []EndUtterance {
	return nil
}

// Close is a no-op for dev-echo.
func (s *devEchoSession) Close(_ context.Context) error {
	return nil
}
