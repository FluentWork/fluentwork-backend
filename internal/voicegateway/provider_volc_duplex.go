package voicegateway

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/voicepoc"
	"github.com/FluentWork/fluentwork-backend/internal/voiceproto"
)

// defaultVolcTurnWait is the maximum time to wait for one user turn result (ASR + TTS).
// 60s gives the Volc duplex API enough headroom for cold-start TTS in the same turn.
// If the server sends no events within this window, the session is considered unhealthy.
const defaultVolcTurnWait = 60 * time.Second

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

		// B15: Try with primary timeout first. WaitTurnResult returns a TurnResult
		// with Outcome set on every exit path (ok/partial/timeout/error). When Outcome
		// is already set, use the partial content even if an error is also returned.
		turn, err := s.session.WaitTurnResult(ctx, s.turnStarted, defaultVolcTurnWait)

		// B15-fix: always send ai.turn.end if the outcome was set, so iOS can leave
		// .processing even when we got no real content. Only return an error to abort
		// the session if the outcome was unset (real transport error, not a timeout).
		if turn.Outcome != "" && turn.Outcome != voicepoc.TurnOutcomeOK {
			s.logger.Warn("turn result non-ok outcome, sending ai.turn.end to unblock iOS",
				"session_id", s.session.SessionID(),
				"outcome", turn.Outcome,
				"has_transcript", turn.Transcript != "",
				"has_text", turn.AssistantText != "",
				"err", err,
			)
			return s.turnToOutbound(turn), nil
		}
		if err == nil {
			return s.turnToOutbound(turn), nil
		}

		// B15: On timeout with partial content, retry once with fresh context (TTS may
		// still be pending on the server side even though the wait expired).
		if errors.Is(err, context.DeadlineExceeded) {
			s.logger.Warn("turn result timeout, retrying with fresh context",
				"session_id", s.session.SessionID(),
				"err", err,
			)
			retryCtx, cancel := context.WithTimeout(context.Background(), 20*time.Second)
			defer cancel()
			turn, retryErr := s.session.WaitTurnResult(retryCtx, s.turnStarted, 20*time.Second)
			// B15-fix: if retry has a set Outcome, prefer it over the error
			if turn.Outcome != "" && turn.Outcome != voicepoc.TurnOutcomeOK {
				s.logger.Warn("retry non-ok outcome, sending ai.turn.end to unblock iOS",
					"session_id", s.session.SessionID(),
					"outcome", turn.Outcome,
					"has_transcript", turn.Transcript != "",
					"has_text", turn.AssistantText != "",
				)
				return s.turnToOutbound(turn), nil
			}
			if retryErr == nil {
				return s.turnToOutbound(turn), nil
			}
			// If retry also failed but returned partial content, use it
			if turn.Transcript != "" || turn.AssistantText != "" {
				s.logger.Warn("turn result recovered from retry with partial content",
					"session_id", s.session.SessionID(),
					"has_transcript", turn.Transcript != "",
					"has_text", turn.AssistantText != "",
				)
				return s.turnToOutbound(turn), nil
			}
			// B15-fix: retry returned no content with an error. Check if Outcome is set.
			if turn.Outcome != "" && turn.Outcome != voicepoc.TurnOutcomeOK {
				s.logger.Warn("retry had non-ok outcome despite error, sending ai.turn.end",
					"session_id", s.session.SessionID(),
					"outcome", turn.Outcome,
				)
				return s.turnToOutbound(turn), nil
			}
			// Both failed with no content, return original error
			return nil, err
		}

		// Non-timeout error, return immediately
		return nil, err

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
		s.logger.Warn("audio format mismatch, dropping binary frame",
			"expected", "pcm-s16le",
			"actual", current,
			"payload_bytes", len(payload),
		)
		return nil, nil // Drop frame silently, don't fail the session
	}
	if len(payload) == 0 {
		return nil, nil
	}
	if s.turnStarted.IsZero() {
		s.turnStarted = time.Now()
	}
	s.logger.Debug("forwarding PCM chunk to volc",
		"payload_bytes", len(payload),
		"session_id", s.session.SessionID(),
	)
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
	// B15: include outcome in all log lines so dashboards can filter by status
	s.logger.Info("turn result captured",
		"transcript_len", len(transcript),
		"transcript", transcript,
		"assistant_text_len", len(strings.TrimSpace(turn.AssistantText)),
		"assistant_text", strings.TrimSpace(turn.AssistantText),
		"active_turn_id", s.activeTurnID,
		"event_types", turn.EventTypes,
		"outcome", turn.Outcome, // B15: explicit outcome in logs
	)
	if transcript != "" {
		s.utterances = append(s.utterances, EndUtterance{
			Seq:     s.nextSeq,
			Speaker: "user",
			Text:    transcript,
		})
		s.nextSeq++
		// B14: relay authoritative provider-side ASR transcript back to the client
		// so the client can use it for B7 hit-detection instead of re-running
		// a separate local ASR pass (e.g., Apple Speech).
		// Also carry it in ServerASRText for backend badge detection.
		outbound = append(outbound, ProviderOutbound{
			Control: voiceproto.ClientASRTranscription{
				Type:   voiceproto.TypeClientASRTranscription,
				Text:   transcript,
				TurnID: s.activeTurnID,
			},
			ServerASRText: transcript, // B14: for badge emitter
		})
	}

	reply := strings.TrimSpace(turn.AssistantText)
	// B15: always send ai.turn.end so iOS can leave .processing even when reply is
	// empty (e.g., timeout with partial ASR transcript but no TTS). Outcome is always
	// set on every exit path in collectTurn, so we can stamp it faithfully here.
	{
		turnID := s.activeTurnID
		if turnID == "" {
			turnID = fmt.Sprintf("volc-turn-%d", s.nextSeq)
		}
		// B15-I3: include the Volcengine vendor log_id so iOS can correlate
		// tracker events with backend and vendor-side diagnostic logs.
		var logID string
		if s.session != nil {
			logID = s.session.LogID()
		}
		outbound = append(outbound, ProviderOutbound{
			Control: voiceproto.AITurnEnd{
				Type:    voiceproto.TypeAITurnEnd,
				TurnID:  turnID,
				Outcome: string(turn.Outcome), // B15: explicit outcome in ai.turn.end
				LogID:   logID, // B15-I3: vendor trace log_id
			},
		})
	}
	if reply != "" {
		turnID := s.activeTurnID
		if turnID == "" {
			turnID = fmt.Sprintf("volc-turn-%d", s.nextSeq)
		}
		outbound = append(outbound, ProviderOutbound{
			Control: map[string]any{
				"type":    voiceproto.TypeAITextDelta,
				"text":    reply,
				"turn_id": turnID,
			},
		})
		s.utterances = append(s.utterances, EndUtterance{
			Seq:     s.nextSeq,
			Speaker: "ai",
			Text:    reply,
		})
		s.nextSeq++
		s.activeTurnID = turnID
	} else {
		// No reply but we sent ai.turn.end above; advance seq so next turn gets a fresh ID.
		s.nextSeq++
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
