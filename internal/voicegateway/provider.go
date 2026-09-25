package voicegateway

import (
	"context"
	"fmt"
	"time"

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
	// Open builds one provider session.
	//
	// audioSeq is the session's frame-number allocator, and it is passed in
	// rather than kept by the provider on purpose: the numbering must survive a
	// provider session being replaced (a transparent reopen), and a value the
	// provider owns is a value somebody has to remember to carry across. The same
	// allocator is handed to every Open call for one client session, so the
	// numbering simply continues. See SeqAllocator.
	//
	// A provider that emits no client-bound audio frames may ignore it.
	Open(ctx context.Context, ticket ConsumedTicket, audioSeq *SeqAllocator, turnRefs *TurnRefAllocator) (VoiceProviderSession, error)
}

// ContinuationTurn is one transcript turn of a previous session, handed to a
// provider so it can open with context instead of from zero.
//
// It rides beside `SessionStart` rather than inside it on purpose: `SessionStart`
// is the wire frame, and this is resolved server-side after an ownership check
// (`session.Service.ContinuationContext`). A field on the frame would be a field
// the client could try to write.
type ContinuationTurn struct {
	Seq     int
	Speaker string
	Text    string
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
	Start(ctx context.Context, start voiceproto.SessionStart, continuation []ContinuationTurn) ([]ProviderOutbound, error)
	HandleClientControl(ctx context.Context, frameType string, raw []byte) ([]ProviderOutbound, error)
	HandleClientAudio(ctx context.Context, payload []byte) ([]ProviderOutbound, error)
	SnapshotUtterances() []EndUtterance
	Close(ctx context.Context) error
}

// StreamingVoiceProviderSession is a VoiceProviderSession that can push
// outbounds while a control call is still in flight.
//
// HandleClientControl returns a slice, so a provider that wants to stream has
// nowhere to put a fragment until the whole call is done — which is exactly why
// the assistant's reply used to arrive only after the turn closed. A session
// implementing this interface is handed an emitter the gateway has already
// wired to the client connection, and may call it at any point during a control
// call.
//
// Implementations may call the emitter from the collect goroutine. The
// gateway serializes writes on sessionRuntime.writeMu, so a sink push
// during WaitTurnResult does not race ping/interrupt on the read loop.
type StreamingVoiceProviderSession interface {
	VoiceProviderSession
	SetOutboundEmitter(emit func(ProviderOutbound) error)
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
//
// The mock's nextSeq numbers its *own* mock playback frames, not client-bound
// audio — it emits none — so it keeps its own counter and ignores the allocator.
func (p MockVoiceProvider) Open(_ context.Context, _ ConsumedTicket, _ *SeqAllocator, _ *TurnRefAllocator) (VoiceProviderSession, error) {
	return &mockVoiceProviderSession{nextSeq: 1, serverASRText: p.ServerASRText}, nil
}

type mockVoiceProviderSession struct {
	nextSeq       int
	utterances    []EndUtterance
	serverASRText string
}

// bootstrapTurnID names the frame every provider emits when the session opens.
//
// It is not a turn. The frame exists so the client can leave `aiSpeaking` — iOS
// enters that state on socketReady and only leaves it on ai.turn.end, so without
// the announcement the user's first tap reads as a barge-in and emits a spurious
// `interrupt`. Nothing was asked of the user, and no turn was taken.
//
// The name is shared rather than repeated per provider so the three cannot drift
// apart on it, and so a test can emit the same frame production does. The
// gateway itself does **not** match on it: what keeps this frame from arming the
// rescue ladder is that no turn spoke, which the turn machine decides on its own
// — see Turn.NoteAIEnd.
const bootstrapTurnID = "bootstrap"

func (s *mockVoiceProviderSession) Start(_ context.Context, _ voiceproto.SessionStart, _ []ContinuationTurn) ([]ProviderOutbound, error) {
	const stub = "ready"
	s.utterances = append(s.utterances, EndUtterance{
		Seq:     s.nextSeq,
		Speaker: "ai",
		Text:    stub,
	})
	s.nextSeq++
	return []ProviderOutbound{
		{
			Control: voiceproto.NewAITextDelta(stub, bootstrapTurnID, time.Now().UnixMilli()),
		},
		{
			Control: voiceproto.AITurnEnd{
				Type:   voiceproto.TypeAITurnEnd,
				TurnID: bootstrapTurnID,
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
	case voiceproto.TypeUserSpeechStart, voiceproto.TypeUserSpeechEnd, voiceproto.TypeInterrupt, voiceproto.TypeClientTurnAbort:
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
