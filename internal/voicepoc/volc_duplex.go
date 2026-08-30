package voicepoc

import (
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"
)

const defaultDuplexEndpoint = "wss://openspeech.bytedance.com/api/v3/duplex/realtime/dialogue"
const defaultDuplexModel = "1.2.6.0"
const defaultDuplexVoice = "zh_female_vv_jupiter_bigtts"

// DuplexConfig configures a Volcano realtime duplex (Seeduplex) session.
// Auth is API-Key only (X-Api-Key); no AppID / Access Token / ResourceId required.
type DuplexConfig struct {
	APIKey   string
	Endpoint string
	Model    string
	Voice    string
	// Instructions is the session system prompt (inject target for B14 V2).
	Instructions string
}

// DuplexSession is one live duplex WebSocket session (JSON text frames).
type DuplexSession struct {
	conn      *websocket.Conn
	sessionID string
	logID     string
	cfg       DuplexConfig
}

// SessionID returns the server session id from session.created.
func (s *DuplexSession) SessionID() string { return s.sessionID }

// LogID returns X-Tt-Logid from the handshake (for vendor support).
func (s *DuplexSession) LogID() string { return s.logID }

// OpenDuplex dials the duplex endpoint, sends session.create, waits for session.created.
func OpenDuplex(ctx context.Context, cfg DuplexConfig) (*DuplexSession, error) {
	if strings.TrimSpace(cfg.APIKey) == "" {
		return nil, fmt.Errorf("duplex API key is required")
	}
	if cfg.Endpoint == "" {
		cfg.Endpoint = defaultDuplexEndpoint
	}
	if cfg.Model == "" {
		cfg.Model = defaultDuplexModel
	}
	if cfg.Voice == "" {
		cfg.Voice = defaultDuplexVoice
	}
	if cfg.Instructions == "" {
		cfg.Instructions = "你是 FluentWork 注入 POC 助手。用简短中文回复。"
	}

	header := http.Header{}
	header.Set("X-Api-Key", cfg.APIKey)
	header.Set("X-Api-Connect-Id", uuid.NewString())

	conn, resp, err := websocket.Dial(ctx, cfg.Endpoint, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		if resp != nil {
			return nil, fmt.Errorf("duplex dial: %w (http=%d logid=%s)", err, resp.StatusCode, resp.Header.Get("X-Tt-Logid"))
		}
		return nil, fmt.Errorf("duplex dial: %w", err)
	}

	s := &DuplexSession{conn: conn, cfg: cfg}
	if resp != nil {
		s.logID = resp.Header.Get("X-Tt-Logid")
	}

	if err := s.send(ctx, map[string]any{
		"type":     "session.create",
		"event_id": uuid.NewString(),
		"session":  s.sessionPayload(cfg.Instructions),
	}); err != nil {
		_ = s.Close(ctx)
		return nil, err
	}

	for {
		evt, err := s.recv(ctx)
		if err != nil {
			_ = s.Close(ctx)
			return nil, fmt.Errorf("wait session.created: %w", err)
		}
		switch evt.Type {
		case "session.created":
			s.sessionID = evt.SessionID
			if s.sessionID == "" {
				_ = s.Close(ctx)
				return nil, fmt.Errorf("session.created missing session.id")
			}
			return s, nil
		case "error":
			_ = s.Close(ctx)
			return nil, fmt.Errorf("duplex error after create: %s", evt.Raw)
		default:
			// ignore stray events before session.created
		}
	}
}

// UpdateInstructions sends session.update — B14 V2 mid-session inject channel.
func (s *DuplexSession) UpdateInstructions(ctx context.Context, instructions string) error {
	if s == nil || s.conn == nil {
		return fmt.Errorf("duplex session is nil")
	}
	if err := s.send(ctx, map[string]any{
		"type":     "session.update",
		"event_id": uuid.NewString(),
		"session":  s.sessionPayload(instructions),
	}); err != nil {
		return err
	}
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		readCtx, cancel := context.WithTimeout(ctx, time.Until(deadline))
		evt, err := s.recv(readCtx)
		cancel()
		if err != nil {
			return fmt.Errorf("wait session.updated: %w", err)
		}
		switch evt.Type {
		case "session.updated":
			return nil
		case "error":
			return fmt.Errorf("duplex error after update: %s", evt.Raw)
		}
	}
	return fmt.Errorf("timeout waiting for session.updated")
}

// Close sends session.close and closes the WebSocket.
func (s *DuplexSession) Close(ctx context.Context) error {
	if s == nil || s.conn == nil {
		return nil
	}
	_ = s.send(ctx, map[string]any{
		"type":     "session.close",
		"event_id": uuid.NewString(),
	})
	err := s.conn.Close(websocket.StatusNormalClosure, "done")
	s.conn = nil
	return err
}

func (s *DuplexSession) sessionPayload(instructions string) map[string]any {
	payload := map[string]any{
		"model":        s.cfg.Model,
		"instructions": instructions,
		"audio": map[string]any{
			"input": map[string]any{
				"format": map[string]any{"type": "pcm", "rate": 16000},
			},
			"output": map[string]any{
				"format": map[string]any{"type": "pcm_s16le", "rate": 24000},
				"voice":  s.cfg.Voice,
			},
		},
	}
	if s.sessionID != "" {
		payload["id"] = s.sessionID
	}
	return payload
}

func (s *DuplexSession) send(ctx context.Context, v any) error {
	b, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return s.conn.Write(ctx, websocket.MessageText, b)
}

type duplexEvent struct {
	Type      string
	SessionID string
	Raw       string
}

func (s *DuplexSession) recv(ctx context.Context) (duplexEvent, error) {
	_, data, err := s.conn.Read(ctx)
	if err != nil {
		return duplexEvent{}, err
	}
	var envelope struct {
		Type    string `json:"type"`
		Session *struct {
			ID string `json:"id"`
		} `json:"session"`
	}
	_ = json.Unmarshal(data, &envelope)
	evt := duplexEvent{Type: envelope.Type, Raw: string(data)}
	if envelope.Session != nil {
		evt.SessionID = envelope.Session.ID
	}
	return evt, nil
}

// SmokeDuplex runs B14 D2: connect → session.create → session.update → close.
// Proves API-Key auth and mid-session inject channel (V2).
func SmokeDuplex(ctx context.Context, cfg DuplexConfig) (map[string]any, error) {
	started := time.Now()
	session, err := OpenDuplex(ctx, cfg)
	if err != nil {
		return nil, err
	}
	defer session.Close(ctx)

	inject := "【B14注入探针】请在后续回复中自然确认用户提到的目标表达；标记词 INJECT_OK。"
	if err := session.UpdateInstructions(ctx, inject); err != nil {
		return nil, err
	}

	return map[string]any{
		"ok":                  true,
		"provider":            "volc-duplex",
		"endpoint":            firstNonEmpty(cfg.Endpoint, defaultDuplexEndpoint),
		"session_id":          session.SessionID(),
		"log_id":              session.LogID(),
		"inject_channel":      "session.update",
		"inject_channel_ok":   true,
		"elapsed_ms":          time.Since(started).Milliseconds(),
		"credential_mode":     "live",
		"notes": []string{
			"D2 PASS: duplex WSS + session.create + session.update",
			"Full T9 delay-gradient still needs audio turn + same-turn observation",
		},
	}, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
