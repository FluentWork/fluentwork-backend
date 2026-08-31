package voicegateway

import (
	"context"
	"fmt"

	"github.com/FluentWork/fluentwork-backend/internal/voiceproto"
)

// ProviderOutbound is one provider->gateway emission.
// Exactly one of Control or Binary should be populated.
type ProviderOutbound struct {
	Control any
	Binary  []byte
}

// VoiceProvider isolates vendor/session orchestration from the gateway loop.
// B13 starts by routing the current mock behavior through this seam so live
// providers can plug in without leaking vendor types into handler.go.
type VoiceProvider interface {
	Open(ctx context.Context, ticket ConsumedTicket) (VoiceProviderSession, error)
}

// VoiceProviderSession owns one gateway session's upstream voice interaction.
type VoiceProviderSession interface {
	Start(ctx context.Context, start voiceproto.SessionStart) ([]ProviderOutbound, error)
	HandleClientControl(ctx context.Context, frameType string, raw []byte) ([]ProviderOutbound, error)
	HandleClientAudio(ctx context.Context, payload []byte) ([]ProviderOutbound, error)
	SnapshotUtterances() []EndUtterance
	Close(ctx context.Context) error
}

// MockVoiceProvider preserves the existing gateway behavior behind the provider seam.
type MockVoiceProvider struct{}

// Open starts a mock provider session for one gateway conversation.
func (MockVoiceProvider) Open(_ context.Context, _ ConsumedTicket) (VoiceProviderSession, error) {
	return &mockVoiceProviderSession{nextSeq: 1}, nil
}

type mockVoiceProviderSession struct {
	nextSeq    int
	utterances []EndUtterance
}

func (s *mockVoiceProviderSession) Start(_ context.Context, _ voiceproto.SessionStart) ([]ProviderOutbound, error) {
	const stub = "ready"
	s.utterances = append(s.utterances, EndUtterance{
		Seq:     s.nextSeq,
		Speaker: "ai",
		Text:    stub,
	})
	s.nextSeq++
	return []ProviderOutbound{{
		Control: map[string]any{
			"type":    voiceproto.TypeAITextDelta,
			"text":    stub,
			"turn_id": "bootstrap",
		},
	}}, nil
}

func (s *mockVoiceProviderSession) HandleClientControl(_ context.Context, frameType string, _ []byte) ([]ProviderOutbound, error) {
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
