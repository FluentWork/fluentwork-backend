package voicegateway

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/voicepoc"
	"github.com/FluentWork/fluentwork-backend/internal/voiceproto"
)

const defaultVolcTurnWait = 35 * time.Second

// VolcDuplexProvider bridges voice-gateway sessions onto the live Volcano duplex API.
// Current scope:
// - opens a real duplex session on session.start
// - forwards raw PCM chunks when configured
// - commits on user.speech.end and returns assistant text
// The current iOS contract still sends framed Opus audio, so the provider fails
// fast with a clear error until the upstream AudioEngine switches gateway input
// to raw PCM or a backend transcode step is added.
type VolcDuplexProvider struct {
	cfg         voicepoc.DuplexConfig
	audioFormat string
	logger      *slog.Logger
}

// NewVolcDuplexProvider constructs the live duplex provider from process config.
func NewVolcDuplexProvider(cfg Config, logger *slog.Logger) VolcDuplexProvider {
	if logger == nil {
		logger = slog.Default()
	}
	return VolcDuplexProvider{
		cfg: voicepoc.DuplexConfig{
			APIKey:   strings.TrimSpace(cfg.VolcSpeechAPIKey),
			Endpoint: strings.TrimSpace(cfg.VolcDuplexEndpoint),
			Model:    strings.TrimSpace(cfg.VolcDuplexModel),
			Voice:    strings.TrimSpace(cfg.VolcDuplexVoice),
			Logger:   logger,
		},
		audioFormat: strings.ToLower(strings.TrimSpace(cfg.ClientAudioFormat)),
		logger:      logger.With("component", "voicegateway.provider.volc_duplex"),
	}
}

// Open creates one live duplex session wrapper.
func (p VolcDuplexProvider) Open(_ context.Context, ticket ConsumedTicket) (VoiceProviderSession, error) {
	if strings.TrimSpace(p.cfg.APIKey) == "" {
		return nil, fmt.Errorf("volc-duplex provider missing speech API key")
	}
	return &volcDuplexProviderSession{
		cfg:         p.cfg,
		audioFormat: p.audioFormat,
		logger: p.logger.With(
			"ticket_id", ticket.TicketID,
			"session_id", ticket.SessionID,
			"user_id", ticket.UserID,
		),
	}, nil
}

type volcDuplexProviderSession struct {
	cfg          voicepoc.DuplexConfig
	audioFormat  string
	logger       *slog.Logger
	session      *voicepoc.DuplexSession
	turnStarted  time.Time
	nextSeq      int
	utterances   []EndUtterance
	activeTurnID string
}

func (s *volcDuplexProviderSession) Start(ctx context.Context, start voiceproto.SessionStart) ([]ProviderOutbound, error) {
	if s.session != nil {
		return nil, nil
	}

	cfg := s.cfg
	if instructions := instructionsForSessionStart(start); instructions != "" {
		cfg.Instructions = instructions
	}
	session, err := voicepoc.OpenDuplex(ctx, cfg)
	if err != nil {
		return nil, err
	}
	s.session = session
	s.nextSeq = 1
	return nil, nil
}

func (s *volcDuplexProviderSession) HandleClientControl(ctx context.Context, frameType string, _ []byte) ([]ProviderOutbound, error) {
	switch frameType {
	case voiceproto.TypeUserSpeechStart:
		if s.session == nil {
			return nil, fmt.Errorf("volc-duplex session not started")
		}
		s.turnStarted = time.Now()
		return nil, nil

	case voiceproto.TypeUserSpeechEnd:
		if s.session == nil {
			return nil, fmt.Errorf("volc-duplex session not started")
		}
		if s.turnStarted.IsZero() {
			s.turnStarted = time.Now()
		}
		if err := s.session.CommitAudio(ctx); err != nil {
			return nil, err
		}
		turn, err := s.session.WaitTurnResult(ctx, s.turnStarted, defaultVolcTurnWait)
		if err != nil {
			return nil, err
		}
		return s.turnToOutbound(turn), nil

	case voiceproto.TypeInterrupt:
		s.logger.Info("interrupt forwarded to live provider boundary")
		return nil, nil

	default:
		return nil, fmt.Errorf("volc-duplex provider does not support control frame %s", frameType)
	}
}

func (s *volcDuplexProviderSession) HandleClientAudio(ctx context.Context, payload []byte) ([]ProviderOutbound, error) {
	if s.session == nil {
		return nil, fmt.Errorf("volc-duplex session not started")
	}
	if strings.TrimSpace(s.audioFormat) != "pcm-s16le" {
		current := strings.TrimSpace(s.audioFormat)
		if current == "" {
			current = "unknown"
		}
		return nil, fmt.Errorf("volc-duplex provider requires VOICE_GATEWAY_CLIENT_AUDIO_FORMAT=pcm-s16le; current=%s", current)
	}
	if len(payload) == 0 {
		return nil, nil
	}
	if s.turnStarted.IsZero() {
		s.turnStarted = time.Now()
	}
	return nil, s.session.AppendPCMChunk(ctx, payload)
}

func (s *volcDuplexProviderSession) SnapshotUtterances() []EndUtterance {
	return append([]EndUtterance(nil), s.utterances...)
}

func (s *volcDuplexProviderSession) Close(ctx context.Context) error {
	if s.session == nil {
		return nil
	}
	return s.session.Close(ctx)
}

func (s *volcDuplexProviderSession) turnToOutbound(turn voicepoc.TurnResult) []ProviderOutbound {
	var outbound []ProviderOutbound
	transcript := strings.TrimSpace(turn.Transcript)
	if transcript != "" {
		s.utterances = append(s.utterances, EndUtterance{
			Seq:     s.nextSeq,
			Speaker: "user",
			Text:    transcript,
		})
		s.nextSeq++
	}

	reply := strings.TrimSpace(turn.AssistantText)
	if reply != "" {
		turnID := fmt.Sprintf("volc-turn-%d", s.nextSeq)
		outbound = append(outbound, ProviderOutbound{
			Control: map[string]any{
				"type":    voiceproto.TypeAITextDelta,
				"text":    reply,
				"turn_id": turnID,
			},
		})
		outbound = append(outbound, ProviderOutbound{
			Control: voiceproto.AITurnEnd{
				Type:   voiceproto.TypeAITurnEnd,
				TurnID: turnID,
			},
		})
		s.utterances = append(s.utterances, EndUtterance{
			Seq:     s.nextSeq,
			Speaker: "ai",
			Text:    reply,
		})
		s.nextSeq++
		s.activeTurnID = turnID
	}
	s.turnStarted = time.Time{}
	return outbound
}

func instructionsForSessionStart(start voiceproto.SessionStart) string {
	var parts []string
	parts = append(parts, "你是 FluentWork 英语口语练习助手。用简短中文或英文回应用户。")
	if scene := strings.TrimSpace(start.SceneType); scene != "" {
		parts = append(parts, "当前练习场景："+scene+"。")
	}
	if material := strings.TrimSpace(start.MaterialID); material != "" {
		parts = append(parts, "素材编号："+material+"。")
	}
	return strings.Join(parts, " ")
}

var (
	_ VoiceProvider        = VolcDuplexProvider{}
	_ VoiceProviderSession = (*volcDuplexProviderSession)(nil)
)
