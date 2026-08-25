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
	logger             *slog.Logger
	now                func() time.Time
	insecureSkipOrigin bool
	idleTimeout        time.Duration
}

// NewHandler constructs the voice gateway HTTP/WSS handler.
func NewHandler(consumer TicketConsumer, lifecycle SessionLifecycle, logger *slog.Logger, opts Options) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	idle := opts.IdleTimeout
	if idle <= 0 {
		idle = defaultIdleTimeout
	}
	return &Handler{
		consumer:           consumer,
		lifecycle:          lifecycle,
		logger:             logger,
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
	authCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	typ, data, err := conn.Read(authCtx)
	if err != nil {
		return ConsumedTicket{}, fmt.Errorf("read auth frame: %w", err)
	}
	if typ != websocket.MessageText {
		return ConsumedTicket{}, errors.New("first frame must be text auth")
	}

	frameType, err := voiceproto.DecodeType(data)
	if err != nil {
		return ConsumedTicket{}, err
	}
	if frameType != voiceproto.TypeAuth {
		return ConsumedTicket{}, fmt.Errorf("expected auth, got %s", frameType)
	}

	var auth voiceproto.Auth
	if err := json.Unmarshal(data, &auth); err != nil {
		return ConsumedTicket{}, fmt.Errorf("decode auth: %w", err)
	}
	ticket := strings.TrimSpace(auth.Ticket)
	if ticket == "" {
		return ConsumedTicket{}, errors.New("ticket is required")
	}
	if h.consumer == nil {
		return ConsumedTicket{}, errors.New("ticket consumer is not configured")
	}

	consumed, err := h.consumer.Consume(authCtx, ticket)
	if err != nil {
		return ConsumedTicket{}, err
	}
	if err := writeJSON(authCtx, conn, voiceproto.SessionReady{
		Type:      voiceproto.TypeSessionReady,
		SessionID: consumed.SessionID,
		UserID:    consumed.UserID,
	}); err != nil {
		return ConsumedTicket{}, err
	}
	return consumed, nil
}

type sessionRuntime struct {
	started   bool
	startedAt time.Time
	nextSeq   int
	turns     []EndUtterance
}

func (h *Handler) loop(ctx context.Context, conn *websocket.Conn, session ConsumedTicket) error {
	rt := &sessionRuntime{nextSeq: 1}
	for {
		readCtx, cancel := context.WithTimeout(ctx, h.idleTimeout)
		typ, data, err := conn.Read(readCtx)
		cancel()
		if err != nil {
			return err
		}
		switch typ {
		case websocket.MessageBinary:
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
			rt.started = true
			rt.startedAt = h.now().UTC()
		}
		h.logger.Info("session.start accepted",
			"session_id", session.SessionID,
			"user_id", session.UserID,
		)
		const stub = "ready"
		rt.turns = append(rt.turns, EndUtterance{
			Seq:     rt.nextSeq,
			Speaker: "ai",
			Text:    stub,
		})
		rt.nextSeq++
		return writeJSON(ctx, conn, map[string]any{
			"type":    voiceproto.TypeAITextDelta,
			"text":    stub,
			"turn_id": "bootstrap",
		})

	case voiceproto.TypeUserSpeechStart, voiceproto.TypeUserSpeechEnd:
		if !rt.started {
			return writeJSON(ctx, conn, voiceproto.ErrorFrame{
				Type:    voiceproto.TypeError,
				Code:    "session_not_started",
				Message: "send session.start first",
			})
		}
		return nil

	case voiceproto.TypeInterrupt:
		h.logger.Info("interrupt received", "session_id", session.SessionID)
		return nil

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
			if err := h.lifecycle.End(endCtx, EndSessionRequest{
				SessionID:   session.SessionID,
				DurationSec: durationSec,
				Reason:      reason,
				Utterances:  append([]EndUtterance(nil), rt.turns...),
			}); err != nil {
				h.logger.Warn("session end persist failed", "session_id", session.SessionID, "err", err)
				return writeJSON(ctx, conn, voiceproto.ErrorFrame{
					Type:    voiceproto.TypeError,
					Code:    "end_failed",
					Message: err.Error(),
				})
			}
		}
		h.logger.Info("session.end persisted",
			"session_id", session.SessionID,
			"duration_sec", durationSec,
			"utterance_count", len(rt.turns),
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
