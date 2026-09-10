// Package voicegateway implements the FluentWork voice WSS gateway (B3/B4).
package voicegateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/FluentWork/fluentwork-backend/internal/voiceproto"
	"github.com/FluentWork/fluentwork-backend/pkg/logx"
)

// warnDedupInterval is how often we emit a deduplicated WARN summary.
// Every Nth occurrence within the window gets a full log line with the count.
const warnDedupInterval = 10

// maxClientBinaryFrame bounds one inbound binary frame. coder/websocket reads
// at most 32 KiB per frame by default, which is far below what this protocol
// allows: the client's 20ms framing is a client choice, and its own per-turn cap
// of 60s is ~1.83 MiB of 16 kHz mono PCM16. A larger frame used to fail the read
// and take the session with it, so the ceiling is raised well past that while
// still bounding what a single frame can make the process allocate.
const maxClientBinaryFrame = 4 << 20

// logWarn emits a WARN log with deduplication. Repeated warnings with the same
// key within `window` are collapsed — only the 1st and every `warnDedupInterval`th
// occurrence produce a full log line. This prevents the 80+ identical WARN lines
// seen in docs/20 §1.1 when the provider is stuck in a timeout loop.
func (h *Handler) logWarn(rt *sessionRuntime, key, msg string, args ...any) {
	now := h.now()
	window := 5 * time.Second
	interval := warnDedupInterval

	if rt.warnDedup.key == key && now.Sub(rt.warnDedup.lastAt) < window {
		rt.warnDedup.count++
		rt.warnDedup.lastAt = now
		if rt.warnDedup.count%interval == 0 {
			// Every 10th repeat: emit a summary instead of repeating the full log.
			summaryArgs := append([]any{"dedup_count", rt.warnDedup.count, "first_key", key}, args...)
			h.logger.Warn(msg+" (deduplicated)", summaryArgs...)
		}
		return
	}
	// New key or outside window — emit full line.
	rt.warnDedup.key = key
	rt.warnDedup.lastAt = now
	rt.warnDedup.count = 1
	h.logger.Warn(msg, args...)
}

var errSessionEnded = errors.New("voice session ended")

// ConsumedTicket is the result of a successful one-time ticket consume.
type ConsumedTicket struct {
	TicketID  string `json:"ticket_id"`
	SessionID string `json:"session_id"`
	UserID    string `json:"user_id"`
}

// TicketConsumer validates and consumes a WSS ticket via app-server.
type TicketConsumer interface {
	Consume(ctx context.Context, rawTicket string) (ConsumedTicket, error)
}

// Options configures optional Handler behavior.
type Options struct {
	// InsecureSkipOrigin skips WebSocket Origin checks (local/dev only).
	InsecureSkipOrigin bool
	// IdleTimeout bounds each WebSocket read; zero uses a 2m default.
	IdleTimeout time.Duration
}

// Handler serves WSS upgrades and the control-frame loop.
type Handler struct {
	consumer           TicketConsumer
	lifecycle          SessionLifecycle
	provider           VoiceProvider
	badgeEmitter       *BadgeEmitter
	logger             *slog.Logger
	now                func() time.Time
	insecureSkipOrigin bool
	idleTimeout        time.Duration
	clientASRRequired  bool // B13: gate user.speech.end with empty text when true
}

// NewHandler constructs the voice gateway HTTP/WSS handler.
func NewHandler(
	consumer TicketConsumer,
	lifecycle SessionLifecycle,
	provider VoiceProvider,
	logger *slog.Logger,
	opts Options,
) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	if provider == nil {
		provider = MockVoiceProvider{}
	}
	idle := opts.IdleTimeout
	if idle <= 0 {
		idle = defaultIdleTimeout
	}
	return &Handler{
		consumer:           consumer,
		lifecycle:          lifecycle,
		provider:           provider,
		logger:             logger.With("component", "voicegateway.handler"),
		now:                time.Now,
		insecureSkipOrigin: opts.InsecureSkipOrigin,
		idleTimeout:        idle,
	}
}

// SetBadgeEmitter wires the optional B12 feedback.badge emitter. Passing nil
// disables hit-detection (the user.speech.end branch becomes a passthrough).
func (h *Handler) SetBadgeEmitter(emitter *BadgeEmitter) {
	h.badgeEmitter = emitter
}

// SetClientASRRequired enables B13 gate: when true, user.speech.end with empty
// text returns an error frame (code: client_asr_required).
func (h *Handler) SetClientASRRequired(required bool) {
	h.clientASRRequired = required
}

// Mount registers health and voice WSS routes on mux.
func (h *Handler) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", h.healthz)
	mux.HandleFunc("GET /readyz", h.healthz)
	mux.HandleFunc("GET /v1/voice", h.serveVoice)
}

func (h *Handler) healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"status":"ok"}`)
}

func (h *Handler) serveVoice(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: h.insecureSkipOrigin,
	})
	if err != nil {
		h.logger.Warn("websocket accept failed", "err", err)
		return
	}
	conn.SetReadLimit(maxClientBinaryFrame)
	defer func() { _ = conn.CloseNow() }()

	ctx := r.Context()
	session, err := h.handshake(ctx, conn)
	if err != nil {
		h.logger.Warn("voice handshake failed", "err", err)
		code := "unauthenticated"
		var ce *ConsumeError
		if errors.As(err, &ce) && ce.Code != "" {
			code = strings.ToLower(ce.Code)
		}
		_ = writeJSON(ctx, conn, voiceproto.ErrorFrame{
			Type:    voiceproto.TypeError,
			Code:    code,
			Message: err.Error(),
		})
		_ = conn.Close(websocket.StatusPolicyViolation, "auth failed")
		return
	}

	h.logger.Info("voice session ready",
		"session_id", session.SessionID,
		"user_id", session.UserID,
	)
	if err := h.loop(ctx, conn, session); err != nil && !errors.Is(err, context.Canceled) {
		h.logger.Info("voice session ended",
			"session_id", session.SessionID,
			"err", err,
		)
	}
}

func (h *Handler) handshake(ctx context.Context, conn *websocket.Conn) (ConsumedTicket, error) {
	seg := logx.Begin(h.logger, "voice.handshake")
	var handshakeErr error
	var endAttrs []any
	defer func() {
		seg.End(handshakeErr, endAttrs...)
	}()

	authCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	typ, data, err := conn.Read(authCtx)
	if err != nil {
		handshakeErr = fmt.Errorf("read auth frame: %w", err)
		return ConsumedTicket{}, handshakeErr
	}
	if typ != websocket.MessageText {
		handshakeErr = errors.New("first frame must be text auth")
		return ConsumedTicket{}, handshakeErr
	}

	frameType, err := voiceproto.DecodeType(data)
	if err != nil {
		handshakeErr = err
		return ConsumedTicket{}, handshakeErr
	}
	if frameType != voiceproto.TypeAuth {
		handshakeErr = fmt.Errorf("expected auth, got %s", frameType)
		return ConsumedTicket{}, handshakeErr
	}

	var auth voiceproto.Auth
	if err := json.Unmarshal(data, &auth); err != nil {
		handshakeErr = fmt.Errorf("decode auth: %w", err)
		return ConsumedTicket{}, handshakeErr
	}
	ticket := strings.TrimSpace(auth.Ticket)
	if ticket == "" {
		handshakeErr = errors.New("ticket is required")
		return ConsumedTicket{}, handshakeErr
	}
	if h.consumer == nil {
		handshakeErr = errors.New("ticket consumer is not configured")
		return ConsumedTicket{}, handshakeErr
	}

	consumed, err := h.consumer.Consume(authCtx, ticket)
	if err != nil {
		handshakeErr = err
		return ConsumedTicket{}, handshakeErr
	}
	if err := writeJSON(authCtx, conn, voiceproto.SessionReady{
		Type:      voiceproto.TypeSessionReady,
		SessionID: consumed.SessionID,
		UserID:    consumed.UserID,
	}); err != nil {
		handshakeErr = err
		return ConsumedTicket{}, handshakeErr
	}
	endAttrs = []any{
		"session_id", consumed.SessionID,
		"user_id", consumed.UserID,
	}
	return consumed, nil
}

type sessionRuntime struct {
	started   bool
	startedAt time.Time
	provider  VoiceProviderSession
	// broken is set the first time the audio forward path fails in a way
	// the session cannot recover from. handleAudio then sends
	// provider_audio_failed and returns a non-nil error so the WSS loop
	// exits immediately (B15 Item 1.2). The flag also drops any binary
	// frame already in flight so we do not re-log the cascade.
	broken bool
	// reopenAttempted records one transparent provider reopen after an
	// upstream audio write failure (backend #43).
	reopenAttempted bool
	// B15: warn deduplication state — prevents 80+ identical WARN lines
	// when the audio forward path fails repeatedly (e.g., provider timeout).
	warnDedup struct {
		key      string
		lastAt   time.Time
		count    int
		window   time.Duration
		interval int // log every Nth occurrence (1, 10, 20, ...)
	}
	// unknownFrameCount is how many well-formed frames with an unknown
	// type this session ignored. Forward-compat: a future v3 type must
	// not kill a v2 gateway the way client.turn.abort once did.
	unknownFrameCount int
	// ended records that this session was already persisted (by the client's
	// `session.end`). The loop exit path must not persist a second time.
	ended bool
}

func (h *Handler) loop(ctx context.Context, conn *websocket.Conn, session ConsumedTicket) (loopErr error) {
	rt := &sessionRuntime{}
	defer func() {
		rt.close(ctx)
		h.persistOnExit(ctx, rt, session, loopErr)
	}()
	for {
		readCtx, cancel := context.WithTimeout(ctx, h.idleTimeout)
		typ, data, err := conn.Read(readCtx)
		cancel()
		if err != nil {
			return err
		}
		switch typ {
		case websocket.MessageBinary:
			if err := h.handleAudio(ctx, conn, data, rt, session); err != nil {
				return err
			}
			continue
		case websocket.MessageText:
			if err := h.handleControl(ctx, conn, session, data, rt); err != nil {
				if errors.Is(err, errSessionEnded) {
					return nil
				}
				return err
			}
		default:
			continue
		}
	}
}

// carryAudioSequence hands the gateway→client frame numbering from a provider
// session being replaced to the one replacing it.
//
// The numbers must not restart across the reopen: the client's barge-in
// watermark outlives the provider session that set it, and it discards every
// frame at or below itself. See SequencedVoiceProviderSession.
//
// Providers that do not number their frames (mock, dev-echo) simply do not
// implement the interface and are left alone.
func carryAudioSequence(previous, next VoiceProviderSession) {
	from, ok := previous.(SequencedVoiceProviderSession)
	if !ok {
		return
	}
	to, ok := next.(SequencedVoiceProviderSession)
	if !ok {
		return
	}
	to.AdoptAudioSequence(from.NextAudioSequence())
}

func (h *Handler) handleAudio(
	ctx context.Context,
	conn *websocket.Conn,
	data []byte,
	rt *sessionRuntime,
	session ConsumedTicket,
) error {
	if !rt.started || rt.provider == nil {
		return nil
	}
	// B15: after the first audio-forward failure we return a non-nil error
	// so the WSS loop exits. This guard is belt-and-suspenders if another
	// binary frame is already in flight: drop it without re-logging.
	if rt.broken {
		return nil
	}
	// Log all incoming binary audio frames for debugging
	h.logger.Debug("received binary audio frame",
		"payload_bytes", len(data),
		"session_started", rt.started,
		"provider_nil", rt.provider == nil,
	)
	outbound, err := rt.provider.HandleClientAudio(ctx, data)
	if err != nil {
		// B15-followup (#43): the first upstream failure may be a dead
		// provider connection (Volc idle / network). Reopen once with the
		// same ticket and retry the chunk before giving up on the session.
		if !rt.reopenAttempted {
			rt.reopenAttempted = true
			reopened, openErr := h.provider.Open(ctx, session)
			if openErr == nil {
				if _, startErr := reopened.Start(ctx, voiceproto.SessionStart{}); startErr == nil {
					carryAudioSequence(rt.provider, reopened)
					rt.provider = reopened
					h.logger.Info("provider reopened after audio forward failure; retrying chunk",
						"session_id", session.SessionID,
						"original_err", err,
					)
					retried, retryErr := rt.provider.HandleClientAudio(ctx, data)
					if retryErr == nil {
						return writeProviderOutbound(ctx, conn, retried)
					}
					err = retryErr
				} else {
					err = startErr
				}
			} else {
				err = openErr
			}
		}
		rt.broken = true
		// B15: use logWarn for deduplication — avoids 80+ identical WARN lines
		// when the provider is stuck in a timeout loop.
		h.logWarn(rt, "provider_audio_failed",
			"provider audio forward failed; session will terminate immediately",
			"err", err,
			"reopen_attempted", rt.reopenAttempted,
		)
		// B15 Item 1.2: send the error frame to notify iOS, then return a real
		// error so the loop exits immediately instead of waiting for idle timeout.
		// If the WS is already dead writeJSON will fail — that's fine, the error
		// return below still terminates the session.
		_ = writeJSON(ctx, conn, voiceproto.ErrorFrame{
			Type:    voiceproto.TypeError,
			Code:    "provider_audio_failed",
			Message: err.Error(),
		})
		return fmt.Errorf("provider audio forward failed: %w", err)
	}
	return writeProviderOutbound(ctx, conn, outbound)
}

func (h *Handler) handleControl(
	ctx context.Context,
	conn *websocket.Conn,
	session ConsumedTicket,
	data []byte,
	rt *sessionRuntime,
) error {
	frameType, err := voiceproto.DecodeType(data)
	if err != nil {
		return writeJSON(ctx, conn, voiceproto.ErrorFrame{
			Type:    voiceproto.TypeError,
			Code:    "invalid_frame",
			Message: err.Error(),
		})
	}

	switch frameType {
	case voiceproto.TypePing:
		var ping voiceproto.Ping
		if err := json.Unmarshal(data, &ping); err != nil {
			return writeJSON(ctx, conn, voiceproto.ErrorFrame{
				Type:    voiceproto.TypeError,
				Code:    "invalid_frame",
				Message: err.Error(),
			})
		}
		ts := ping.TS
		if ts == 0 {
			ts = h.now().UnixMilli()
		}
		return writeJSON(ctx, conn, voiceproto.Pong{Type: voiceproto.TypePong, TS: ts})

	case voiceproto.TypeSessionStart:
		var start voiceproto.SessionStart
		if err := json.Unmarshal(data, &start); err != nil {
			return writeJSON(ctx, conn, voiceproto.ErrorFrame{
				Type:    voiceproto.TypeError,
				Code:    "invalid_frame",
				Message: err.Error(),
			})
		}
		if !rt.started {
			if h.lifecycle != nil {
				if err := h.lifecycle.Activate(ctx, session.SessionID); err != nil {
					h.logger.Warn("session activate failed", "session_id", session.SessionID, "err", err)
					return writeJSON(ctx, conn, voiceproto.ErrorFrame{
						Type:    voiceproto.TypeError,
						Code:    "activate_failed",
						Message: err.Error(),
					})
				}
			}
			provider, err := h.provider.Open(ctx, session)
			if err != nil {
				h.logger.Warn("provider open failed", "session_id", session.SessionID, "err", err)
				return writeJSON(ctx, conn, voiceproto.ErrorFrame{
					Type:    voiceproto.TypeError,
					Code:    "provider_open_failed",
					Message: err.Error(),
				})
			}
			rt.provider = provider
			rt.started = true
			rt.startedAt = h.now().UTC()
		}
		h.logger.Info("session.start accepted",
			"session_id", session.SessionID,
			"user_id", session.UserID,
			"stage", "orchestration",
		)
		outbound, err := rt.provider.Start(ctx, start)
		if err != nil {
			h.logger.Warn("provider start failed", "session_id", session.SessionID, "err", err)
			return writeJSON(ctx, conn, voiceproto.ErrorFrame{
				Type:    voiceproto.TypeError,
				Code:    "provider_start_failed",
				Message: err.Error(),
			})
		}
		return writeProviderOutbound(ctx, conn, outbound)

	case voiceproto.TypeUserSpeechStart, voiceproto.TypeUserSpeechEnd:
		if !rt.started {
			return writeJSON(ctx, conn, voiceproto.ErrorFrame{
				Type:    voiceproto.TypeError,
				Code:    "session_not_started",
				Message: "send session.start first",
			})
		}
		h.logger.Info("voice user speech frame",
			"session_id", session.SessionID,
			"type", frameType,
			"stage", "asr",
		)
		// The transparent reopen budget belongs to a turn, not to the session.
		// A session-lifetime budget was exhausted by the first upstream hiccup,
		// and since volc-duplex loses its duplex roughly once per turn, the
		// next failure killed the session outright.
		if frameType == voiceproto.TypeUserSpeechStart && rt.reopenAttempted {
			rt.reopenAttempted = false
			h.logger.Info("reopen budget refilled for the new turn",
				"session_id", session.SessionID,
				"stage", "orchestration",
			)
		}
		outbound, err := rt.provider.HandleClientControl(ctx, frameType, data)
		if err != nil {
			h.logger.Warn("provider control forward failed",
				"session_id", session.SessionID,
				"type", frameType,
				"err", err,
			)
			return writeJSON(ctx, conn, voiceproto.ErrorFrame{
				Type:    voiceproto.TypeError,
				Code:    "provider_control_failed",
				Message: err.Error(),
			})
		}
		if err := writeProviderOutbound(ctx, conn, outbound); err != nil {
			return err
		}
		// B13 gate: when client ASR is required and text is empty, reject early.
		if frameType == voiceproto.TypeUserSpeechEnd && h.clientASRRequired {
			var end voiceproto.UserSpeechEnd
			if jsonErr := json.Unmarshal(data, &end); jsonErr == nil {
				if strings.TrimSpace(end.Text) == "" {
					return writeJSON(ctx, conn, voiceproto.ErrorFrame{
						Type:    voiceproto.TypeError,
						Code:    "client_asr_required",
						Message: "user.speech.end.text is required when VOICE_CLIENT_ASR_REQUIRED is enabled",
					})
				}
			}
		}
		// B12 — fire-and-forget hit detection on user.speech.end so the
		// client ASR transcript can be matched against stored phrase blocks.
		// Never blocks the control frame; failures are logged by the emitter.
		// B14: When client text is empty (B13 client ASR disabled), fall back to
		// server-side ASR text from the provider's ProviderOutbound.ServerASRText.
		if frameType == voiceproto.TypeUserSpeechEnd && h.badgeEmitter != nil {
			var end voiceproto.UserSpeechEnd
			if jsonErr := json.Unmarshal(data, &end); jsonErr == nil {
				turnID := strings.TrimSpace(end.TurnID)
				if turnID == "" {
					// No client-supplied turn id → scope dedupe by session.
					turnID = session.SessionID
				}
				// Prefer client ASR text; fall back to server ASR text (B14).
				asrText := strings.TrimSpace(end.Text)
				if asrText == "" {
					asrText = extractServerASRText(outbound)
				}
				h.badgeEmitter.Emit(ctx, realBadgeConn{conn}, session.UserID, session.SessionID, turnID, asrText)
			}
		}
		return nil

	case voiceproto.TypeClientTurnAbort:
		// I20: recording abort. iOS already stopped PCM and will not send
		// user.speech.end. Accepting this frame (instead of unsupported_frame)
		// keeps the session alive for the next user.speech.start.
		var abort voiceproto.ClientTurnAbort
		if err := json.Unmarshal(data, &abort); err != nil {
			return writeJSON(ctx, conn, voiceproto.ErrorFrame{
				Type:    voiceproto.TypeError,
				Code:    "invalid_frame",
				Message: err.Error(),
			})
		}
		if !voiceproto.ValidClientTurnAbortOutcome(abort.Outcome) {
			return writeJSON(ctx, conn, voiceproto.ErrorFrame{
				Type:    voiceproto.TypeError,
				Code:    "invalid_frame",
				Message: "client.turn.abort.outcome must be timeout, user_abandoned, or error",
			})
		}
		h.logger.Info("client.turn.abort accepted",
			"session_id", session.SessionID,
			"turn_id", strings.TrimSpace(abort.TurnID),
			"outcome", abort.Outcome,
			"stage", "asr",
		)
		if !rt.started || rt.provider == nil {
			return nil
		}
		outbound, err := rt.provider.HandleClientControl(ctx, frameType, data)
		if err != nil {
			// Do not emit error frames here: iOS maps them to .failed and
			// would kill the session this frame exists to keep alive.
			h.logger.Warn("provider turn abort forward failed; session stays open",
				"session_id", session.SessionID,
				"turn_id", strings.TrimSpace(abort.TurnID),
				"err", err,
			)
			return nil
		}
		return writeProviderOutbound(ctx, conn, outbound)

	case voiceproto.TypeInterrupt:
		h.logger.Info("interrupt received", "session_id", session.SessionID, "stage", "orchestration")
		if !rt.started || rt.provider == nil {
			return nil
		}
		outbound, err := rt.provider.HandleClientControl(ctx, frameType, data)
		if err != nil {
			h.logger.Warn("provider interrupt forward failed", "session_id", session.SessionID, "err", err)
			return writeJSON(ctx, conn, voiceproto.ErrorFrame{
				Type:    voiceproto.TypeError,
				Code:    "provider_interrupt_failed",
				Message: err.Error(),
			})
		}
		return writeProviderOutbound(ctx, conn, outbound)

	case voiceproto.TypeSessionEnd:
		var end voiceproto.SessionEnd
		if err := json.Unmarshal(data, &end); err != nil {
			h.logger.Warn("session.end frame decode failed", "err", err, "raw", string(data))
		}
		reason := strings.TrimSpace(end.Reason)
		if reason == "" {
			reason = "user"
		}
		durationSec, err := h.persistSession(ctx, rt, session, reason)
		if err != nil {
			h.logger.Warn("session end persist failed", "session_id", session.SessionID, "err", err)
			return writeJSON(ctx, conn, voiceproto.ErrorFrame{
				Type:    voiceproto.TypeError,
				Code:    "end_failed",
				Message: err.Error(),
			})
		}
		rt.ended = true
		h.logger.Info("session.end persisted",
			"session_id", session.SessionID,
			"duration_sec", durationSec,
			"utterance_count", len(rt.snapshotUtterances()),
			"unknown_frame_count", rt.unknownFrameCount,
			"stage", "orchestration",
		)
		_ = writeJSON(ctx, conn, map[string]any{
			"type":   voiceproto.TypeSessionEnd,
			"reason": "ack",
		})
		_ = conn.Close(websocket.StatusNormalClosure, "session ended")
		return errSessionEnded

	case voiceproto.TypeAuth:
		return writeJSON(ctx, conn, voiceproto.ErrorFrame{
			Type:    voiceproto.TypeError,
			Code:    "already_authenticated",
			Message: "auth already completed",
		})

	default:
		// Ignore unknown types. Emitting unsupported_frame used to map to
		// iOS .failed and killed live sessions (client.turn.abort before
		// 5c2e39f). A later protocol version adding a new client frame
		// must not repeat that on an older gateway.
		rt.unknownFrameCount++
		h.logWarn(rt, "unknown_frame",
			"ignoring unknown control frame",
			"session_id", session.SessionID,
			"type", frameType,
			"unknown_frame_count", rt.unknownFrameCount,
		)
		return nil
	}
}

func writeJSON(ctx context.Context, conn *websocket.Conn, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, raw)
}

func writeProviderOutbound(ctx context.Context, conn *websocket.Conn, outbound []ProviderOutbound) error {
	for _, item := range outbound {
		switch {
		case item.Control != nil:
			if err := writeJSON(ctx, conn, item.Control); err != nil {
				return err
			}
		case len(item.Binary) > 0:
			if err := conn.Write(ctx, websocket.MessageBinary, item.Binary); err != nil {
				return err
			}
		}
	}
	return nil
}

// extractServerASRText returns the server-side ASR text from provider outbound.
// B14: The Volcengine provider populates ServerASRText in ProviderOutbound so
// the handler can use it for badge detection when client ASR text is empty.
func extractServerASRText(outbound []ProviderOutbound) string {
	for _, item := range outbound {
		if item.ServerASRText != "" {
			return item.ServerASRText
		}
	}
	return ""
}

func (r *sessionRuntime) snapshotUtterances() []EndUtterance {
	if r.provider == nil {
		return nil
	}
	return r.provider.SnapshotUtterances()
}

// snapshotVoiceUsage reports the audio the provider moved, when it can say.
//
// Nil for providers that carry no real conversation (mock, dev-echo): the field
// is optional on the wire for the same reason, and an unreported session must
// leave no cost row rather than a zero-valued one.
func (r *sessionRuntime) snapshotVoiceUsage() *VoiceUsage {
	reporter, ok := r.provider.(VoiceUsageReporter)
	if !ok {
		return nil
	}
	usage := reporter.VoiceUsage()
	return &usage
}

// persistSession writes the session and its utterances through app-server and
// returns the duration it recorded. A nil lifecycle is a no-op: local runs
// without app-server still get the rest of the session behaviour.
func (h *Handler) persistSession(
	ctx context.Context,
	rt *sessionRuntime,
	session ConsumedTicket,
	reason string,
) (int, error) {
	durationSec := 0
	if rt.started && !rt.startedAt.IsZero() {
		durationSec = int(h.now().UTC().Sub(rt.startedAt).Seconds())
		if durationSec < 0 {
			durationSec = 0
		}
	}
	if h.lifecycle == nil {
		return durationSec, nil
	}
	endCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	utterances := append([]EndUtterance(nil), rt.snapshotUtterances()...)
	return durationSec, h.lifecycle.End(endCtx, EndSessionRequest{
		SessionID:   session.SessionID,
		DurationSec: durationSec,
		Reason:      reason,
		Utterances:  utterances,
		VoiceUsage:  rt.snapshotVoiceUsage(),
	})
}

// persistOnExit writes the session when the WSS loop ends without a client
// `session.end`. Without it a session killed by a provider failure loses every
// utterance — no rows, no session.finished job, no review.
func (h *Handler) persistOnExit(
	ctx context.Context,
	rt *sessionRuntime,
	session ConsumedTicket,
	loopErr error,
) {
	if h.lifecycle == nil || !rt.started || rt.ended {
		return
	}
	reason := "connection_closed"
	switch {
	case rt.broken:
		reason = "provider_audio_failed"
	case errors.Is(loopErr, context.DeadlineExceeded):
		reason = "idle_timeout"
	}
	// By the time the loop unwinds the connection context is already done.
	// Keep its values (log context) but drop its cancellation, otherwise the
	// app-server call would fail before it left the process.
	_, err := h.persistSession(context.WithoutCancel(ctx), rt, session, reason)
	if err != nil {
		h.logger.Warn("session exit persist failed",
			"session_id", session.SessionID, "reason", reason, "err", err)
		return
	}
	rt.ended = true
	h.logger.Info("session.exit persisted",
		"session_id", session.SessionID,
		"duration_sec", int(h.now().UTC().Sub(rt.startedAt).Seconds()),
		"reason", reason,
		"utterance_count", len(rt.snapshotUtterances()),
		"unknown_frame_count", rt.unknownFrameCount,
		"stage", "orchestration",
	)
}

func (r *sessionRuntime) close(ctx context.Context) {
	if r.provider == nil {
		return
	}
	// Same reason as persistOnExit: a cancelled parent would make this a
	// no-op and leak the upstream vendor connection on every error exit.
	closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
	defer cancel()
	_ = r.provider.Close(closeCtx)
}
