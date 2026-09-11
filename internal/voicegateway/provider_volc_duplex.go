package voicegateway

import (
	"context"
	"encoding/binary"
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
	cfg         voicepoc.DuplexConfig
	audioFormat string
	logger      *slog.Logger
	session     *voicepoc.DuplexSession
	turnStarted time.Time
	nextSeq     int
	// nextAudioSeq numbers the gateway→client binary audio frames. Monotonic
	// across the session because the client drops frames at or below its
	// barge-in watermark.
	nextAudioSeq uint32
	utterances   []EndUtterance
	activeTurnID string
	// audio moved this session, for cost accounting. See VoiceUsage.
	usage voiceUsage
	// inputMuted tracks whether the vendor has been told the microphone is
	// silent. Not bookkeeping for its own sake: the event must be sent once per
	// silent stretch, and sending unmute without a preceding mute (or twice in
	// a row) is a protocol violation the vendor is entitled to reject.
	inputMuted bool
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

	// emit is the gateway's push path, installed via SetOutboundEmitter. Nil
	// means the gateway does not support streaming, in which case this session
	// must fall back to emitting the reply once at turn end.
	emit func(ProviderOutbound) error
	// emitErr records the first push failure. It is not returned from
	// HandleClientControl: a failed push means the client connection is gone,
	// and the handler finds out on its own next write. Recorded so the failure
	// is not silent, logged once in turnToOutbound.
	emitErr error
	// deliveredText accumulates the assistant text actually pushed to the client
	// this turn. On a barge-in it is the record of what the user heard, which is
	// what the transcript should contain — the rest was never spoken to them.
	deliveredText strings.Builder
	// interruptedThisTurn is set by a client `interrupt` and read once, at turn
	// end, when the assistant utterance is written.
	interruptedThisTurn bool
	// streamedText is set when this turn's reply was already pushed as deltas,
	// so turnToOutbound must not send it a second time — the client appends
	// ai.text.delta to the open AI item, so a repeat would duplicate the text.
	streamedText bool
	// streamedAudio is the same idea for the assistant's voice: pushed frame by
	// frame during the turn, so turnToOutbound must not send the whole turn's
	// audio again.
	streamedAudio bool
	// audioResampler converts vendor chunks to the client's playback rate as
	// they arrive. Rebuilt each turn, which is what keeps the output
	// byte-identical to the batch form it replaced: that one converted each
	// turn's audio independently.
	audioResampler *pcmResampler
	// audioPending holds resampled bytes that do not yet fill a client frame.
	// Resampled output does not align to the frame size, so this is carried
	// across chunks — a short frame per vendor chunk would hand the client
	// ragged frames for the whole turn.
	audioPending []byte
}

// SetOutboundEmitter implements StreamingVoiceProviderSession. The gateway
// calls it whenever it (re)assigns a provider session.
func (s *volcDuplexProviderSession) SetOutboundEmitter(emit func(ProviderOutbound) error) {
	s.emit = emit
	s.wireTurnSink()
}

// wireTurnSink points the live duplex session's streaming sink at this
// provider session. Called after every assignment to s.session — a reopen
// builds a fresh duplex, which would otherwise stream nowhere.
//
// The sink is installed only when an emitter is. Without somewhere to push,
// streaming would suppress the end-of-turn full-text delta (see
// turnToOutbound) and the client would get no reply at all.
func (s *volcDuplexProviderSession) wireTurnSink() {
	if s.session == nil {
		return
	}
	if s.emit == nil {
		s.session.SetTurnSink(nil)
		return
	}
	s.session.SetTurnSink(s)
}

// AssistantTextDelta implements voicepoc.TurnSink: one fragment of the
// assistant's reply, pushed the moment the vendor produces it.
func (s *volcDuplexProviderSession) AssistantTextDelta(delta string) {
	if delta == "" || s.emit == nil {
		return
	}
	s.streamedText = true
	s.deliveredText.WriteString(delta)
	// Same id the end-of-turn frames will carry. activeTurnID is set at
	// user.speech.end and nextSeq does not move again until turnToOutbound, so
	// this resolves to the same value the terminal frames use.
	turnID := canonicalTurnID(s.activeTurnID, s.nextSeq)
	if err := s.emit(ProviderOutbound{
		Control: voiceproto.NewAITextDelta(delta, turnID, s.unixMilli()),
	}); err != nil && s.emitErr == nil {
		s.emitErr = err
	}
}

// resetTurnStreamingState clears everything that belongs to a single turn.
//
// Called at turn start and on abort, never at turn end — turnToOutbound still
// needs to read these to know whether to re-send anything.
func (s *volcDuplexProviderSession) resetTurnStreamingState() {
	s.streamedText = false
	s.streamedAudio = false
	s.deliveredText.Reset()
	s.interruptedThisTurn = false
	s.audioResampler = nil
	s.audioPending = nil
}

// AssistantAudio implements voicepoc.TurnSink: one chunk of the assistant's
// speech, pushed the moment the vendor produces it.
//
// This is the half of P1-2 that a user actually hears. Text deltas made the
// transcript appear sooner; audio frames are what make the assistant *speak*
// sooner. Before this, the whole turn's audio was resampled and cut into frames
// at turn end, so the first syllable waited for the last one.
func (s *volcDuplexProviderSession) AssistantAudio(pcm []byte) {
	if len(pcm) == 0 || s.emit == nil {
		return
	}
	if !s.streamedAudio {
		s.markFirstAudio()
	}
	s.streamedAudio = true
	for _, frame := range s.frameAudio(pcm, false) {
		if err := s.emit(ProviderOutbound{Binary: frame}); err != nil && s.emitErr == nil {
			s.emitErr = err
		}
	}
}

// markFirstAudio records the gateway's own instant for the first audio frame of
// a turn — the moment the learner actually starts hearing the assistant.
//
// P1-21: `server_ts_ms` rides on `ai.text.delta` alone, so a first *text* frame
// can be split into upstream / server / downstream while the first *audio*
// frame — the one the user perceives — could only ever be reported as a single
// total. There was no server-side instant to subtract.
//
// # Why this is a log and not a wire field
//
// The obvious carrier already exists and cannot be used. `ai.tts.start` is
// deliberately **not** sent by this provider: `TTSFrameDispatcher` only claims
// binary frames once it has seen one, and the decoder bound into it does not
// drive `AVAudioEngine` — emitting it would move the audio onto a path that
// makes no sound. (See the ordering note in `turnToOutbound`.)
//
// So the alternatives were a new control frame, widening every binary audio
// frame for a stamp that matters once per turn, or a log. A log costs nothing
// on the wire, and these two logs are already correlated by `session_id` /
// `turn_id` — the client's total minus `server_ms` is the network's share,
// which is the split this record exists to make possible.
//
// `server_ts_ms` is absolute so the join does not depend on either clock being
// the same: with the ping-derived offset the client can place it on its own
// clock, exactly as it does for `ai.text.delta`.
func (s *volcDuplexProviderSession) markFirstAudio() {
	if s.logger == nil || s.turnStarted.IsZero() {
		return
	}
	now := s.unixMilli()
	sessionID := ""
	if s.session != nil {
		sessionID = s.session.SessionID()
	}
	s.logger.Info("voice.duplex.first_audio",
		"module", "voicegateway",
		"stage", "tts",
		"session_id", sessionID,
		"turn_id", canonicalTurnID(s.activeTurnID, s.nextSeq),
		"server_ts_ms", now,
		"server_ms", now-s.turnStarted.UTC().UnixMilli(),
	)
}

// frameAudio resamples one vendor chunk and cuts it into client frames.
//
// When flush is set, a trailing partial frame is emitted too. That has to
// happen exactly once, at turn end: without it the last few milliseconds of the
// assistant's speech would sit in audioPending forever — clipped, and then
// prepended to the *next* turn's audio.
func (s *volcDuplexProviderSession) frameAudio(pcm []byte, flush bool) [][]byte {
	if s.audioResampler == nil {
		s.audioResampler = &pcmResampler{}
	}
	s.audioPending = append(s.audioPending, s.audioResampler.Write(pcm)...)

	var frames [][]byte
	for len(s.audioPending) >= audioFrameBytes {
		s.nextAudioSeq++
		frames = append(frames, encodeAudioFrame(s.nextAudioSeq, s.audioPending[:audioFrameBytes]))
		s.audioPending = s.audioPending[audioFrameBytes:]
	}
	if flush && len(s.audioPending) > 0 {
		s.nextAudioSeq++
		frames = append(frames, encodeAudioFrame(s.nextAudioSeq, s.audioPending))
		s.audioPending = nil
	}
	return frames
}

func (s *volcDuplexProviderSession) Start(ctx context.Context, start voiceproto.SessionStart, continuation []ContinuationTurn) ([]ProviderOutbound, error) {
	if s.session != nil {
		return nil, nil
	}

	if instructions := instructionsForSessionStart(start, continuation); instructions != "" {
		s.cfg.Instructions = instructions
	}
	session, err := voicepoc.OpenDuplex(ctx, s.cfg)
	if err != nil {
		return nil, err
	}
	s.session = session
	s.wireTurnSink()
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
		// A new turn begins here. Resetting at turn *start* rather than only at
		// turn end is what makes it safe: an aborted turn never reaches
		// turnToOutbound, and state left over from it would corrupt the next
		// turn — a stale streamed flag would suppress its reply entirely, and
		// stale audioPending would prepend the previous turn's tail to it.
		s.resetTurnStreamingState()
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

		// The client goes silent from here until the next turn — it sends PCM
		// only inside a speech window (docs/40) — so declare the microphone
		// muted. Without this the vendor keeps waiting for uplink audio that
		// will never come, times out, and stops responding.
		//
		// Note the division of labour with `DuplexSession.sendSilence`, which
		// already pumps 20ms silence frames to keep the uplink warm: that runs
		// only *during* collectTurn and stops when it returns. The gap this
		// event covers is the one between turns, which nothing else fills.
		if !s.inputMuted {
			if err := s.session.CommitInputMute(ctx); err != nil {
				return nil, err
			}
			s.inputMuted = true
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
		s.resetTurnStreamingState()
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
		// The user cut this reply off. Everything already pushed to the client
		// stays; everything after this point is audio the client discards at
		// its barge-in watermark, so it was never heard and must not reach the
		// transcript as if it had been.
		s.interruptedThisTurn = true
		s.logger.Info("interrupt forwarded to live provider boundary",
			"delivered_chars", s.deliveredText.Len())
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
	// Audio is arriving again, so the microphone is no longer silent. Unmute
	// before the first frame of the new turn: the vendor was told at the last
	// turn end that nothing more was coming, and leaving that standing would
	// make this turn look like it arrived out of nowhere.
	if s.inputMuted {
		if err := s.session.CommitInputUnmute(ctx); err != nil {
			return nil, err
		}
		s.inputMuted = false
	}
	// Counted here, after the format and empty-payload guards, so a dropped
	// frame is not billed as audio that reached the vendor.
	s.usage.addUplink(len(payload))
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
	// Reset here rather than inside defaultResetDuplex: resetDuplexFn is an
	// injectable seam, and the invariant ("a replaced duplex has been told
	// nothing") belongs to *any* reset, not to one implementation of it. Placing
	// it in the default left a custom reset carrying the old session's mute
	// state, so the new session would never be unmuted. Caught by
	// TestVolcDuplexResetClearsMuteState.
	s.inputMuted = false
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
	// The fresh duplex has no sink; without this the reopened session would
	// stop streaming for the rest of the run.
	s.wireTurnSink()
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
		// The utterance is recorded either way — it is the persisted record of
		// the turn. The frame is not: when the reply already went out as deltas,
		// sending the whole thing again would append it a second time on the
		// client, whose reducer appends ai.text.delta to the open AI item.
		if !s.streamedText {
			outbound = append(outbound, ProviderOutbound{
				Control: voiceproto.NewAITextDelta(reply, turnID, s.unixMilli()),
			})
		}
		// What the user heard, not what the vendor produced.
		//
		// An interrupted reply is cut at the point of the barge-in: the audio
		// after it was dropped by the client, so recording the whole reply
		// would put words in the transcript that nobody ever heard. When the
		// user heard nothing at all there is no assistant turn to record —
		// an empty row would claim the assistant spoke.
		spoken := reply
		record := true
		if s.interruptedThisTurn {
			spoken = strings.TrimSpace(s.deliveredText.String())
			// Nothing had reached the client, so the user heard nothing. An
			// empty row would claim the assistant spoke.
			record = spoken != ""
			if !record {
				s.logger.Info("interrupted before any delivery; no assistant turn recorded",
					"turn_id", turnID)
			}
		}
		if record {
			s.utterances = append(s.utterances, EndUtterance{
				Seq:         s.nextSeq,
				Speaker:     "ai",
				Text:        spoken,
				Interrupted: s.interruptedThisTurn,
			})
		}
		s.nextSeq++
		s.activeTurnID = turnID
	} else {
		// No reply but we sent ai.turn.end above; advance seq so next turn gets a fresh ID.
		s.nextSeq++
	}
	// The assistant's voice.
	//
	// Ordering note: this used to be documented as "last, so it follows
	// ai.turn.end". When the audio streams, most of it now precedes
	// ai.turn.end — that is the entire point, since the client plays frames as
	// they arrive and `ai.turn.end` only finalizes the transcript item. The two
	// are independent on the client, so the reordering is deliberate.
	//
	// Sent as plain binary frames with **no** ai.tts.start on purpose.
	// TTSFrameDispatcher only claims binary frames once it has seen an
	// ai.tts.start, and the decoder bound into it (MockTTSDecoder) records
	// without driving AVAudioEngine — its own doc says so. With no ai.tts.start
	// the middleware falls back to `audioEngine.play(frame:)`, which is the path
	// that actually makes sound today. Move this onto the ai.tts.* stream once a
	// decoder that really decodes lands.
	// The vendor's own output, before the 24k→16k resample: that is the audio
	// the vendor produced and will bill for.
	s.usage.addDownlink(len(turn.AudioPCM))
	if s.streamedAudio {
		// Already on the wire, frame by frame. What remains is the trailing
		// partial frame the resampler held back — flushed here, exactly once,
		// because frameAudio cannot know it is the last one until now.
		for _, frame := range s.frameAudio(nil, true) {
			outbound = append(outbound, ProviderOutbound{Binary: frame})
		}
	} else if pcm := resampleToPlaybackRate(turn.AudioPCM); len(pcm) > 0 {
		for offset := 0; offset < len(pcm); offset += audioFrameBytes {
			end := offset + audioFrameBytes
			if end > len(pcm) {
				end = len(pcm)
			}
			// Pre-increment: the counter belongs to the emission path, so it is
			// correct whether or not Start() ran. A first frame numbered 0 would
			// also sit at the client's barge-in watermark.
			s.nextAudioSeq++
			outbound = append(outbound, ProviderOutbound{
				Binary: encodeAudioFrame(s.nextAudioSeq, pcm[offset:end]),
			})
		}
	}
	// Terminate the audio stream explicitly.
	//
	// P1-16: this frame used to be emitted only by the local mock, so on the
	// production path the client could not tell "the assistant finished
	// speaking" from "the assistant got stuck" — both present as audio that
	// stopped arriving. A stream needs a terminator (chunked encoding, SSE's
	// `[DONE]`, a WebSocket close frame); inferring the end from silence is the
	// classic version of this mistake, and it is what pushes an implementer to
	// approximate the signal with a timer that is early or late for every reply.
	//
	// Emitted even when the turn produced no audio: "finished with nothing to
	// say" and "still going" are different, and only one of them is a problem.
	//
	// Safe to send precisely because `ai.tts.start` is not: `TTSDecoder` keeps
	// its stream `.idle` until it sees a start, and the `.idle` branch of
	// `ai.tts.end` is a no-op — so this adds a terminator without moving the
	// audio onto the decoder path. See the ordering note above `turnToOutbound`.
	outbound = append(outbound, ProviderOutbound{
		Control: voiceproto.AITTSEnd{
			Type:             voiceproto.TypeAITTSEnd,
			TurnID:           canonicalTurnID(s.activeTurnID, s.nextSeq),
			CompletionStatus: ttsCompletionStatus(turn.Outcome),
			DurationMs:       audioDurationMs(len(turn.AudioPCM)),
		},
	})

	// A push that failed during the turn means the client connection went away
	// mid-reply. Not returned as an error — the handler's own next write fails
	// on the same dead socket — but never swallowed either.
	if s.emitErr != nil {
		// logger already carries session_id / ticket_id / user_id.
		s.logger.Warn("streaming push failed during turn", "err", s.emitErr)
		s.emitErr = nil
	}
	s.resetTurnStreamingState()
	s.turnStarted = time.Time{}
	return outbound
}

// duplexOutputRate is the vendor's fixed output rate: the duplex protocol
// accepts only 24 kHz for output audio, so this is not a choice we get to make.
const duplexOutputRate = 24000

// clientPlaybackRate is what the client plays. `LiveAudioEngine` builds every
// playback buffer with its own `targetFormat` (16 kHz mono int16) and memcpys
// the payload straight in, so bytes at any other rate come out at the wrong
// speed and pitch.
const clientPlaybackRate = 16000

// audioFrameBytes is ~100 ms of 16 kHz mono PCM16. The chunking is load-bearing:
// the client's `AudioPlaybackGate` drops whole frames at and below the barge-in
// watermark, so one frame per turn (a 10s reply is 320 KB) would leave nothing
// to drop.
const audioFrameBytes = 3200

// encodeAudioFrame is the gateway→client binary layout: UInt32 big-endian
// sequence followed by the payload, matching the client's WSAudioFrameCodec.
// audioFrameHeaderBytes is the big-endian uint32 sequence number that precedes
// every gateway→client binary audio frame.
const audioFrameHeaderBytes = 4

func encodeAudioFrame(seq uint32, payload []byte) []byte {
	frame := make([]byte, audioFrameHeaderBytes+len(payload))
	binary.BigEndian.PutUint32(frame, seq)
	copy(frame[audioFrameHeaderBytes:], payload)
	return frame
}

// The gateway carries this session's frame numbering into any session opened to
// replace it, and nothing else. If these methods go away the reopen silently
// restarts the sequence and mutes the client — see the interface doc.
var _ SequencedVoiceProviderSession = (*volcDuplexProviderSession)(nil)

// VoiceUsage implements VoiceUsageReporter. Read at session end, when the
// gateway hands the session's totals to cost accounting.
func (s *volcDuplexProviderSession) VoiceUsage() VoiceUsage {
	usage := s.usage.measure()
	usage.Model = strings.TrimSpace(s.cfg.Model)
	return usage
}

var _ VoiceUsageReporter = (*volcDuplexProviderSession)(nil)

// NextAudioSequence implements SequencedVoiceProviderSession.
func (s *volcDuplexProviderSession) NextAudioSequence() uint32 { return s.nextAudioSeq }

// AdoptAudioSequence implements SequencedVoiceProviderSession. A session opened
// to replace this one has to keep numbering from here — see the interface doc
// for why restarting is not survivable.
func (s *volcDuplexProviderSession) AdoptAudioSequence(seq uint32) { s.nextAudioSeq = seq }

// resampleToPlaybackRate converts the vendor's 24 kHz output to the 16 kHz the
// client plays: a straight 3:2 linear interpolation.
//
// Deliberately plain, and there is no anti-alias filter ahead of the
// decimation — the vendor's speech carries content up to 12 kHz and this folds
// the 8–12 kHz band back down. Speech stays intelligible, and this is a first
// cut to get sound working end to end. If quality becomes a complaint, replace
// this with a polyphase filter rather than tuning the interpolation.
// resampleToPlaybackRate converts a whole buffer in one go.
//
// It is a thin wrapper over pcmResampler on purpose: the batch and streaming
// paths are the same code, so they cannot drift. The old standalone
// implementation lives on in the tests as the reference the streaming form is
// checked against (see batchResampleReference).
func resampleToPlaybackRate(pcm []byte) []byte {
	var r pcmResampler
	return r.Write(pcm)
}

func instructionsForSessionStart(start voiceproto.SessionStart, continuation []ContinuationTurn) string {
	var parts []string
	parts = append(parts, "你是 FluentWork 英语口语练习助手。用简短中文或英文回应用户。")
	if scene := strings.TrimSpace(start.SceneType); scene != "" {
		parts = append(parts, "当前练习场景："+scene+"。")
	}
	if material := strings.TrimSpace(start.MaterialID); material != "" {
		parts = append(parts, "素材编号："+material+"。")
	}
	if block := continuationBlock(continuation); block != "" {
		parts = append(parts, block)
	}
	return strings.Join(parts, " ")
}

// continuationBlock renders a previous session's tail for the system prompt.
//
// The prompt states the facts and asks for one behaviour — pick up where the
// conversation left off and say what that was. It deliberately does **not**
// script the opening line: the PRD's example ("上次我们聊到限流方案，今天继续？")
// is what the model should produce from the transcript, and hard-coding a
// sentence would produce it whether or not it matched what was actually said.
//
// `speaker` comes from the store and is written as-is rather than mapped
// through a table here. A new value reaching the model labelled `system` is
// more useful than one relabelled `ai`.
func continuationBlock(turns []ContinuationTurn) string {
	if len(turns) == 0 {
		return ""
	}
	lines := make([]string, 0, len(turns)+3)
	lines = append(lines,
		"这是同一用户的上一场练习，以下是它的末尾几轮对话（最早在前）：",
	)
	for _, t := range turns {
		text := strings.TrimSpace(t.Text)
		if text == "" {
			continue
		}
		lines = append(lines, t.Speaker+": "+text)
	}
	if len(lines) == 1 {
		return ""
	}
	lines = append(lines, "开场时用一句话自然承接上面聊到的内容，让用户知道你还记得，然后继续这次练习。")
	return strings.Join(lines, "\n")
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

// ttsCompletionStatus describes how an audio stream ended, in the vocabulary
// `ai.tts.end` already uses. Falling back to "ok" for an unset outcome keeps
// the field non-empty: an empty status would read as "unknown", which is the
// one thing this marker exists to rule out.
func ttsCompletionStatus(outcome voicepoc.TurnOutcome) string {
	if outcome == "" {
		return string(voicepoc.TurnOutcomeOK)
	}
	return string(outcome)
}

// audioDurationMs converts vendor audio bytes to milliseconds.
//
// The vendor's output rate is fixed at 24 kHz mono s16le, so a byte count is an
// exact duration: 24000 samples/s * 2 bytes = 48 bytes per millisecond.
func audioDurationMs(bytes int) *int {
	if bytes <= 0 {
		return nil
	}
	ms := bytes / (2 * duplexOutputRate / 1000)
	return &ms
}
