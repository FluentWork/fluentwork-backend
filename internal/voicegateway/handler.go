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
	// broken is set to true the first time the audio forward path fails in a
	// way the session cannot recover from (provider write error, recv-side
	// failure surfaced through the provider, etc.). Once set, subsequent
	// binary frames are dropped silently so a stuck iOS pipeline doesn't
	// produce 80+ identical WARN lines per session. See docs/20 §1.2.c/1.3.
	broken bool
}

func (h *Handler) loop(ctx context.Context, conn *websocket.Conn, session ConsumedTicket) error {
	rt := &sessionRuntime{}
	defer rt.close(ctx)
	for {
		readCtx, cancel := context.WithTimeout(ctx, h.idleTimeout)
		typ, data, err := conn.Read(readCtx)
		cancel()
		if err != nil {
			return err
		}
		switch typ {
		case websocket.MessageBinary:
			if err := h.handleAudio(ctx, conn, data, rt); err != nil {
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

func (h *Handler) handleAudio(
	ctx context.Context,
	conn *websocket.Conn,
	data []byte,
	rt *sessionRuntime,
) error {
	if !rt.started || rt.provider == nil {
		return nil
	}
	// B15: once the audio forward path has failed, drop further binary
	// frames without re-invoking the provider or re-logging. The loop will
	// exit naturally when iOS closes the WS or the next read errors out.
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
		rt.broken = true
		// B15: one deduplicated warn per session instead of one per frame.
		// See docs/20 §1.3 ("warn 日志去重 / 采样 — 80+ identical warns").
		h.logger.Warn("provider audio forward failed; marking session broken and dropping further audio",
			"err", err,
		)
		// Try to surface the failure to iOS once. If the iOS WS is also dead
		// the write will fail; in that case the next loop iteration will hit
		// the rt.broken guard above and bail out silently until the read
		// fails or iOS closes the connection.
		return writeJSON(ctx, conn, voiceproto.ErrorFrame{
			Type:    voiceproto.TypeError,
			Code:    "provider_audio_failed",
			Message: err.Error(),
		})
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
		durationSec := 0
		if rt.started && !rt.startedAt.IsZero() {
			durationSec = int(h.now().UTC().Sub(rt.startedAt).Seconds())
			if durationSec < 0 {
				durationSec = 0
			}
		}
		if h.lifecycle != nil {
			endCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
			defer cancel()
			utterances := append([]EndUtterance(nil), rt.snapshotUtterances()...)
			if err := h.lifecycle.End(endCtx, EndSessionRequest{
				SessionID:   session.SessionID,
				DurationSec: durationSec,
				Reason:      reason,
				Utterances:  utterances,
			}); err != nil {
				h.logger.Warn("session end persist failed", "session_id", session.SessionID, "err", err)
				return writeJSON(ctx, conn, voiceproto.ErrorFrame{
					Type:    voiceproto.TypeError,
					Code:    "end_failed",
					Message: err.Error(),
				})
			}
		}
		utterances := rt.snapshotUtterances()
		h.logger.Info("session.end persisted",
			"session_id", session.SessionID,
			"duration_sec", durationSec,
			"utterance_count", len(utterances),
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
		return writeJSON(ctx, conn, voiceproto.ErrorFrame{
			Type:    voiceproto.TypeError,
			Code:    "unsupported_frame",
			Message: fmt.Sprintf("unsupported type %s", frameType),
		})
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

func (r *sessionRuntime) close(ctx context.Context) {
	if r.provider == nil {
		return
	}
	closeCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
	defer cancel()
	_ = r.provider.Close(closeCtx)
}
