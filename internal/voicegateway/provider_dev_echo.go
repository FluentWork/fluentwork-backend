package voicegateway

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"io"
	"log/slog"
	"os"
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
// T2 (B15): When FixturePath is set, the provider also streams pre-recorded
// PCM audio back to the client after user.speech.end, simulating AI speech.
// This enables local E2E testing of the audio capture/playback path without
// a live Volcengine session.
//
// This provider MUST NOT be selectable in production builds — keep it
// behind `VOICE_GATEWAY_PROVIDER=dev-echo` and document that fact in
// the env example. The provider name is intentionally verbose so it
// can't be confused with a real provider in incident response.
type DevEchoVoiceProvider struct {
	EchoText     string
	FixturePath  string // T2: optional path to a 16kHz mono PCM fixture file
	Logger       *slog.Logger
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
	var fixture io.ReadCloser
	if p.FixturePath != "" {
		f, err := os.Open(p.FixturePath)
		if err != nil {
			p.Logger.Warn("dev-echo fixture file not found, proceeding without audio fixture",
				"session_id", ticket.SessionID,
				"fixture_path", p.FixturePath,
				"err", err,
			)
		} else {
			fixture = f
		}
	}
	return &devEchoSession{
		echoText:     p.EchoText,
		fixture:     fixture,
		nextSeq:      1,
	}, nil
}

// 20ms of 16 kHz mono s16le.
const devEchoChunkBytes = 640

type devEchoSession struct {
	echoText string
	fixture io.ReadCloser
	nextSeq int
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
				Type:    voiceproto.TypeAITurnEnd,
				TurnID:  "bootstrap",
				Outcome: "ok",
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
//
// T2: When a PCM fixture is loaded, this also starts streaming audio chunks
// back to the client as the first ProviderOutbound item.
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
	if echoText == "" && s.fixture == nil {
		// No authoritative transcript and no fixture → no relay frame and no badge,
		// but the turn must still end so iOS does not hang in `.processing`.
		// B15: outcome="ok" so iOS knows this is a clean end, not a timeout.
		return []ProviderOutbound{
			{
				Control: voiceproto.AITurnEnd{
					Type:    voiceproto.TypeAITurnEnd,
					TurnID:  turnID,
					Outcome: "ok",
				},
			},
		}, nil
	}

	var outbound []ProviderOutbound

	// T2: Stream the first audio fixture chunk back as an audio binary frame.
	// The handler will continue reading from the fixture on subsequent
	// HandleClientAudio calls until EOF, at which point we send ai.turn.end.
	if s.fixture != nil {
		chunk := make([]byte, devEchoChunkBytes)
		n, _ := s.fixture.Read(chunk)
		if n > 0 {
			outbound = append(outbound, ProviderOutbound{
				Binary: chunk[:n],
			})
		}
	}

	if echoText != "" {
		outbound = append(outbound, ProviderOutbound{
			Control: voiceproto.ClientASRTranscription{
				Type:   voiceproto.TypeClientASRTranscription,
				Text:   echoText,
				TurnID: turnID,
			},
			// B14: this is the authoritative text the gateway will feed
			// into the B12 hit detector when the client sends `text: nil`.
			ServerASRText: echoText,
		})
	}

	// T2: if fixture is exhausted, close it and send ai.turn.end.
	if s.fixture != nil {
		// Try to read one more chunk to see if we're at EOF.
		// If Read returns 0/EOF, close fixture and end turn.
		check := make([]byte, devEchoChunkBytes)
		n, err := s.fixture.Read(check)
		if n == 0 || (err != nil && err != io.EOF) {
			if err := s.fixture.Close(); err == nil {
				s.fixture = nil
			}
			// Fixture exhausted — end the turn so iOS leaves .processing.
			outbound = append(outbound, ProviderOutbound{
				Control: voiceproto.AITurnEnd{
					Type:    voiceproto.TypeAITurnEnd,
					TurnID:  turnID,
					Outcome: "ok",
				},
			})
		} else if n > 0 {
			// More chunks remain — send this one too.
			outbound = append(outbound, ProviderOutbound{
				Binary: check[:n],
			})
		}
	}

	return outbound, nil
}

// HandleClientAudio (T2): continues streaming the PCM fixture back to the client
// one chunk at a time. When the fixture is exhausted, sends ai.turn.end to
// unblock the iOS state machine.
func (s *devEchoSession) HandleClientAudio(_ context.Context, data []byte) ([]ProviderOutbound, error) {
	// Consume the incoming audio data (we don't use it in dev-echo, but we
	// must drain it so the handler doesn't keep calling us for the same chunk).
	_ = data

	if s.fixture == nil {
		return nil, nil
	}

	chunk := make([]byte, devEchoChunkBytes)
	n, err := s.fixture.Read(chunk)
	if n == 0 || (err != nil && err != io.EOF) {
		// Fixture exhausted or read error — close and end turn.
		if err := s.fixture.Close(); err == nil {
			s.fixture = nil
		}
		turnID := "dev-echo-turn"
		return []ProviderOutbound{
			{
				Control: voiceproto.AITurnEnd{
					Type:    voiceproto.TypeAITurnEnd,
					TurnID:  turnID,
					Outcome: "ok",
				},
			},
		}, nil
	}

	return []ProviderOutbound{
		{
			Binary: chunk[:n],
		},
	}, nil
}

// SnapshotUtterances is empty — dev sessions do not build a real
// utterance timeline.
func (s *devEchoSession) SnapshotUtterances() []EndUtterance {
	return nil
}

// Close closes the fixture file if still open.
func (s *devEchoSession) Close(_ context.Context) error {
	if s.fixture != nil {
		s.fixture.Close()
		s.fixture = nil
	}
	return nil
}

// FixturePCMLoader loads a 16kHz mono PCM file for the dev-echo fixture.
// Used by tests and by cmd/voice-gateway when VOICE_DEV_ECHO_FIXTURE is set.
// Returns the raw PCM bytes (WAV header stripped if present).
func FixturePCMLoader(path string) ([]byte, error) {
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	// If it's a WAV file, strip the 44-byte RIFF header.
	if len(data) > 44 && string(data[0:4]) == "RIFF" && string(data[8:12]) == "WAVE" {
		// Try to find the data chunk and extract it.
		// Simple approach: skip 44 bytes (standard PCM WAV header).
		return data[44:], nil
	}
	return data, nil
}

// DevEchoFixtureGenerator generates a synthetic PCM fixture for testing.
// Produces a 1kHz sine wave at 16kHz mono PCM16, of the requested duration.
func DevEchoFixtureGenerator(durationMs int) []byte {
	// 16kHz, 16-bit mono.
	const sampleRate = 16000
	frameCount := sampleRate * durationMs / 1000
	pcm := make([]byte, frameCount*2) // 16-bit = 2 bytes per sample
	for i := 0; i < frameCount; i++ {
		// 1kHz sine wave, amplitude 16000 (leaving headroom).
		sample := int16(16000 * sineWave(float64(i)/float64(sampleRate)*1000))
		binary.LittleEndian.PutUint16(pcm[i*2:], uint16(sample))
	}
	return pcm
}

func sineWave(freq float64) float64 {
	const tau = 2 * 3.141592653589793
	// Simple sine without importing math — use float math.
	return sineApprox(tau * freq)
}

func sineApprox(x float64) float64 {
	// Normalize x to [0, 2π).
	const tau = 2 * 3.141592653589793
	x = x - float64(int(x/tau))*tau
	if x < 0 {
		x += tau
	}
	// Taylor series for sin(x).
	x = x - tau*float64(int(x/tau+0.5)) // fold into [-π, π]
	// Already in range after the fold above.
	// sin(x) ≈ x - x³/6 + x⁵/120 - x⁷/5040 (7th-order Taylor)
	x2 := x * x
	return x*(1 - x2*(1.0/6.0 - x2*(1.0/120.0 - x2*(1.0/5040.0))))
}

// Ensure io and binary are used (we already import them above).
