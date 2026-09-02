package voicegateway

import (
	"context"
	"fmt"

	"github.com/FluentWork/fluentwork-backend/internal/voiceproto"
)

// ProviderOutbound is one provider->gateway emission.
// Exactly one of Control or Binary should be populated.
// B14: ServerASRText carries the authoritative provider-side transcript
// for badge hit detection when the client sends user.speech.end with empty
// text. Any VoiceProviderSession implementation may set this on any
// ProviderOutbound in a single HandleClientControl response; the gateway
// scans the produced outbounds and pulls the first non-empty ServerASRText
// for badge emission (see extractServerASRText).
type ProviderOutbound struct {
	Control       any
	Binary        []byte
	ServerASRText string // B14: authoritative ASR text for badge detection
}

// VoiceProvider isolates vendor/session orchestration from the gateway loop.
// B13 starts by routing the current mock behavior through this seam so live
// providers can plug in without leaking vendor types into handler.go.
type VoiceProvider interface {
	Open(ctx context.Context, ticket ConsumedTicket) (VoiceProviderSession, error)
}

// VoiceProviderSession owns one gateway session's upstream voice interaction.
//
// B14 contract: HandleClientControl may return outbounds with ServerASRText
// populated. The gateway uses the first non-empty ServerASRText returned for
// the same control frame (typically user.speech.end) as the badge-detection
// input. Providers that do not run server-side ASR (e.g. MockVoiceProvider,
// relay-only transports) leave ServerASRText empty and the gateway falls
// back to whatever the client supplied on user.speech.end.
type VoiceProviderSession interface {
	Start(ctx context.Context, start voiceproto.SessionStart) ([]ProviderOutbound, error)
	HandleClientControl(ctx context.Context, frameType string, raw []byte) ([]ProviderOutbound, error)
	HandleClientAudio(ctx context.Context, payload []byte) ([]ProviderOutbound, error)
	SnapshotUtterances() []EndUtterance
	Close(ctx context.Context) error
}

// MockVoiceProvider preserves the existing gateway behavior behind the provider seam.
type MockVoiceProvider struct {
	// ServerASRText, when non-empty, causes the mock session to echo it back
	// as a ClientASRTranscription + ServerASRText ProviderOutbound on every
	// HandleClientControl call. This lets tests exercise the B14 server-side
	// ASR fallback path without standing up a real provider.
	ServerASRText string
}

// Open starts a mock provider session for one gateway conversation.
func (p MockVoiceProvider) Open(_ context.Context, _ ConsumedTicket) (VoiceProviderSession, error) {
	return &mockVoiceProviderSession{nextSeq: 1, serverASRText: p.ServerASRText}, nil
}

type mockVoiceProviderSession struct {
	nextSeq       int
	utterances    []EndUtterance
	serverASRText string
}

func (s *mockVoiceProviderSession) Start(_ context.Context, _ voiceproto.SessionStart) ([]ProviderOutbound, error) {
	const stub = "ready"
	s.utterances = append(s.utterances, EndUtterance{
		Seq:     s.nextSeq,
		Speaker: "ai",
		Text:    stub,
	})
	s.nextSeq++
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

func (s *mockVoiceProviderSession) HandleClientControl(_ context.Context, frameType string, _ []byte) ([]ProviderOutbound, error) {
	if s.serverASRText != "" {
		return []ProviderOutbound{
			{
				Control: voiceproto.ClientASRTranscription{
					Type:   voiceproto.TypeClientASRTranscription,
					Text:   s.serverASRText,
					TurnID: "mock-turn",
				},
				ServerASRText: s.serverASRText, // B14: for badge detection
			},
		}, nil
	}
	switch frameType {
	case voiceproto.TypeUserSpeechStart, voiceproto.TypeUserSpeechEnd, voiceproto.TypeInterrupt:
		return nil, nil
	default:
		return nil, fmt.Errorf("mock provider does not support control frame %s", frameType)
	}
}

func (s *mockVoiceProviderSession) HandleClientAudio(_ context.Context, _ []byte) ([]ProviderOutbound, error) {
	return nil, nil
}

func (s *mockVoiceProviderSession) SnapshotUtterances() []EndUtterance {
	return append([]EndUtterance(nil), s.utterances...)
}

func (s *mockVoiceProviderSession) Close(_ context.Context) error {
	return nil
}
