package voicegateway

import (
	"context"
	"encoding/json"
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

// keepaliveIdleThreshold is how long a Volc Duplex session may be quiet
// before we treat the next audio forward as "potentially stale" and probe
// the upstream first. The threshold mirrors Volc's own idle-disconnect
// behavior (≈2 min on the duplex API as observed in 2026-09-03 production),
// with a comfortable margin so the probe fires before the upstream gives up.
// B15-followup (#43): real-device evidence was 2 min idle → broken pipe on
// resume; 60s gives us 2 attempts at the probe before the upstream closes.
const keepaliveIdleThreshold = 60 * time.Second

// keepaliveProbeTimeout caps one upstream write used purely to detect liveness.
// Kept short (3s) so a dead upstream still fails the audio forward within
// budget — iOS sees provider_audio_failed within ~3s instead of waiting for
// the 60s turn deadline.
const keepaliveProbeTimeout = 3 * time.Second

type volcDuplexProviderSession struct {
	cfg          voicepoc.DuplexConfig
	audioFormat  string
	logger       *slog.Logger
	session      *voicepoc.DuplexSession
	turnStarted  time.Time
	nextSeq      int
	utterances   []EndUtterance
	activeTurnID string
	// B15-followup (#43): when lastAudioAt is older than keepaliveIdleThreshold,
	// the next HandleClientAudio call probes the upstream with an empty commit
	// before forwarding the real payload. Probing first (instead of reacting to
	// the inevitable broken pipe) lets us reopen transparently on the same
	// audio chunk and avoid the iOS-visible 录音失败 / provider_audio_failed.
	lastAudioAt time.Time
	// probeFn is the upstream-liveness probe used when the session has been
	// idle past keepaliveIdleThreshold. It defaults to session.CommitAudio
	// (an idempotent empty-buffer commit) but is overridable in tests so the
	// keepalive decision logic can be exercised without a real Volc socket.
	probeFn func(context.Context) error
	// nowFn lets tests freeze the clock for the idle-since calculation.
	nowFn func() time.Time
	// resetDuplexFn replaces defaultResetDuplex in tests. Abort uses it to
	// drop the upstream audio buffer when Volc has no documented clear event.
	resetDuplexFn func(context.Context) error
}

func (s *volcDuplexProviderSession) Start(ctx context.Context, start voiceproto.SessionStart) ([]ProviderOutbound, error) {
	if s.session != nil {
		return nil, nil
	}

	if instructions := instructionsForSessionStart(start); instructions != "" {
		s.cfg.Instructions = instructions
	}
	session, err := voicepoc.OpenDuplex(ctx, s.cfg)
	if err != nil {
		return nil, err
	}
	s.session = session
	s.nextSeq = 1
	if s.nowFn == nil {
		s.nowFn = time.Now
	}
	// Announce that the session is open. iOS enters `aiSpeaking` on socketReady
	// and only leaves it on `ai.turn.end`, so without this a volc-backed session
	// sits in `aiSpeaking` until the first turn ends — the user's first tap is
	// then read as barge-in and emits a spurious `interrupt` (I20 Item 4). Mock
	// and DevEcho already emit this bootstrap frame; volc-duplex did not.
	//
	// Deliberately no LogID and no ai.text.delta:
	//   - iOS keeps the first non-empty log_id for the whole session, and this
	//     duplex is not the one the first turn runs on after an abort reset
	//     (`resetDuplex` opens a new session), so stamping it here would break
	//     the turn_id/log_id pairing from docs/35.
	//   - a synthetic text delta would put words in the model's mouth in the
	//     user-visible transcript. DevEcho's "ready" stub is fine there; a real
	//     session must not fabricate assistant text.
	return []ProviderOutbound{{
		Control: voiceproto.AITurnEnd{
			Type:    voiceproto.TypeAITurnEnd,
			TurnID:  "bootstrap",
			Outcome: "ok",
		},
	}}, nil
}

func (s *volcDuplexProviderSession) HandleClientControl(ctx context.Context, frameType string, data []byte) ([]ProviderOutbound, error) {
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
		// B12-followup (#42): parse the client-supplied turn_id so badges,
		// ai.turn.end and client.asr.transcription all carry the same id that
		// iOS uses for its local dedupe mirror (runbook Case 3). Without this,
		// the first turn used to fall back to "volc-turn-<seq>" which is offset by 1
		// from iOS's turn-1… naming and breaks cross-layer correlation.
		var end voiceproto.UserSpeechEnd
		if len(data) > 0 {
			if jsonErr := json.Unmarshal(data, &end); jsonErr == nil {
				if t := strings.TrimSpace(end.TurnID); t != "" {
					s.activeTurnID = t
				}
			}
		}
		if s.session != nil {
			s.session.SetClientTurnID(s.activeTurnID)
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

		// The turn's read failed: the upstream socket is gone. Replace it here,
		// where the death is detected, instead of leaving the corpse for the
		// next audio write to discover — that path spends the handler's
		// transparent reopen budget on a connection already known to be dead.
		// A provider error *event* is different: the session is still usable
		// and resetting would discard the server-side conversation context.
		if errors.Is(err, voicepoc.ErrDuplexClosed) {
			s.resetAfterTurnReadFailure(ctx)
		}

		// B15-fix: always send ai.turn.end if the outcome was set, so iOS can leave
		// .processing even when we got no real content. collectTurn stamps Outcome
		// on every exit path, so timeout/partial/error take this branch and skip
		// the 20s retry below. That retry is only for DeadlineExceeded with an
		// unset Outcome (transport timeout before collectTurn could classify).
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

	case voiceproto.TypeClientTurnAbort:
		// Cancel the open user-speech window without CommitAudio / collectTurn.
		// The next user.speech.start begins a fresh turn; WSS stays open.
		s.turnStarted = time.Time{}
		s.activeTurnID = ""
		if err := s.resetDuplex(ctx); err != nil {
			// Keep the iOS WSS alive. The leftover Volc buffer is a residual
			// risk logged here; the next audio-forward reopen-once may recover.
			s.logger.Warn("abort duplex reset failed; next turn may see leftover audio",
				"err", err,
			)
		} else {
			s.logger.Info("client.turn.abort dropped in-progress user speech; duplex reset")
		}
		return nil, nil

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
	// B15-followup (#43): if we've been idle past the keepalive threshold, the
	// Volc upstream may have closed the duplex. Forwarding raw PCM into a dead
	// pipe surfaces as "write tcp ... broken pipe" and the handler marks the
	// session broken — iOS then sees 录音失败. Probe first with a cheap empty
	// commit so we detect the dead upstream before the user audio arrives and
	// can return an error the handler uses to trigger its reopen path.
	if s.shouldProbe() {
		if err := s.runProbe(ctx); err != nil {
			s.logger.Warn("upstream probe failed after idle; signaling handler to reopen",
				"session_id", s.session.SessionID(),
				"idle_since", s.lastAudioAt,
				"err", err,
			)
			return nil, fmt.Errorf("volc upstream probe failed after %s idle: %w", s.idleSince().Round(time.Second), err)
		}
		s.logger.Info("upstream probe ok after idle; resuming audio forward",
			"session_id", s.session.SessionID(),
			"idle", s.idleSince().Round(time.Second),
		)
	}
	s.lastAudioAt = s.nowFn()
	return nil, s.session.AppendPCMChunk(ctx, payload)
}

// shouldProbe reports whether the keepalive probe must run before the next
// audio forward. The probe is only meaningful when (a) we've seen at least one
// prior audio chunk (lastAudioAt != zero) and (b) the gap since then exceeds
// keepaliveIdleThreshold. A fresh session (no prior audio) is never probed —
// the upstream is necessarily alive because the very first chunk created it.
func (s *volcDuplexProviderSession) shouldProbe() bool {
	if s.lastAudioAt.IsZero() {
		return false
	}
	return s.idleSince() > keepaliveIdleThreshold
}

// idleSince returns how long it has been since the last audio forward. Uses
// nowFn when set so tests can drive the clock deterministically.
func (s *volcDuplexProviderSession) idleSince() time.Duration {
	now := s.nowFn
	if now == nil {
		now = time.Now
	}
	return now().Sub(s.lastAudioAt)
}

// runProbe dispatches to the injectable probe function or falls back to the
// default CommitAudio-based probe. Keeping the indirection here lets tests
// inject deterministic success/failure without spinning up a fake Volc socket.
func (s *volcDuplexProviderSession) runProbe(parent context.Context) error {
	probe := s.probeFn
	if probe == nil {
		probe = s.defaultProbe
	}
	ctx, cancel := context.WithTimeout(parent, keepaliveProbeTimeout)
	defer cancel()
	return probe(ctx)
}

// defaultProbe sends a no-op commit to detect whether the Volc duplex
// upstream is still alive. The duplex API treats input_audio_buffer.commit as
// safe to send on an empty buffer; if the underlying WebSocket has been
// closed server-side, this write surfaces the broken pipe immediately
// instead of waiting for the next user payload to trigger it.
//
// Errors returned here are *real* transport failures — they do not indicate
// a normal "nothing to commit" condition. Callers should treat any error as
// "upstream is dead, reopen the session".
func (s *volcDuplexProviderSession) defaultProbe(ctx context.Context) error {
	if s.session == nil {
		return fmt.Errorf("volc-duplex session not started")
	}
	return s.session.CommitAudio(ctx)
}

const duplexResetTimeout = 8 * time.Second

// resetAfterTurnReadFailure replaces a duplex that a turn found dead. Failure
// to reset is not fatal: the next audio write still triggers the handler's
// reopen-once path, so the session degrades to today's behaviour rather than
// breaking.
func (s *volcDuplexProviderSession) resetAfterTurnReadFailure(ctx context.Context) {
	if err := s.resetDuplex(ctx); err != nil {
		s.logger.Warn("duplex reset after turn read failure failed; next audio write will reopen",
			"err", err,
		)
		return
	}
	s.logger.Info("duplex reset after turn read failure")
}

func (s *volcDuplexProviderSession) resetDuplex(ctx context.Context) error {
	if s.resetDuplexFn != nil {
		return s.resetDuplexFn(ctx)
	}
	return s.defaultResetDuplex(ctx)
}

// defaultResetDuplex opens a new Volc duplex session then closes the old one.
// Official duplex events list append/commit only — no input_audio_buffer.clear
// (volc docs 6561/2549778 / Seeduplex client events). Reconnect is the
// isolation fallback so aborted PCM cannot contaminate the next ASR turn.
func (s *volcDuplexProviderSession) defaultResetDuplex(ctx context.Context) error {
	if s.session == nil {
		return nil
	}
	resetCtx, cancel := context.WithTimeout(ctx, duplexResetTimeout)
	defer cancel()
	newSess, err := voicepoc.OpenDuplex(resetCtx, s.cfg)
	if err != nil {
		return err
	}
	old := s.session
	s.session = newSess
	if old != nil {
		closeCtx, closeCancel := context.WithTimeout(context.Background(), 3*time.Second)
		_ = old.Close(closeCtx)
		closeCancel()
	}
	return nil
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
		turnID := canonicalTurnID(s.activeTurnID, s.nextSeq)
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
				LogID:   logID,                // B15-I3: vendor trace log_id
			},
		})
	}
	if reply != "" {
		turnID := canonicalTurnID(s.activeTurnID, s.nextSeq)
		outbound = append(outbound, ProviderOutbound{
			Control: voiceproto.NewAITextDelta(reply, turnID, s.unixMilli()),
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

func (s *volcDuplexProviderSession) unixMilli() int64 {
	now := s.nowFn
	if now == nil {
		now = time.Now
	}
	return now().UTC().UnixMilli()
}

var (
	_ VoiceProvider        = VolcDuplexProvider{}
	_ VoiceProviderSession = (*volcDuplexProviderSession)(nil)
)
