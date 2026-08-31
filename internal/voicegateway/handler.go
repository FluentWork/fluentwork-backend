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
	logger             *slog.Logger
	now                func() time.Time
	insecureSkipOrigin bool
	idleTimeout        time.Duration
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
	outbound, err := rt.provider.HandleClientAudio(ctx, data)
	if err != nil {
		h.logger.Warn("provider audio forward failed", "err", err)
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
