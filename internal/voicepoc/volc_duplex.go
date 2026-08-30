package voicepoc

import (
	"context"
	"encoding/base64"
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

// 20ms of 16 kHz mono s16le.
const pcm16kChunkBytes = 640

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

// SendPCM streams 16 kHz mono s16le PCM as 20ms input_audio_buffer.append frames.
func (s *DuplexSession) SendPCM(ctx context.Context, pcm []byte) error {
	if s == nil || s.conn == nil {
		return fmt.Errorf("duplex session is nil")
	}
	for i := 0; i < len(pcm); i += pcm16kChunkBytes {
		end := i + pcm16kChunkBytes
		if end > len(pcm) {
			end = len(pcm)
		}
		chunk := pcm[i:end]
		if err := s.send(ctx, map[string]any{
			"type":     "input_audio_buffer.append",
			"event_id": uuid.NewString(),
			"audio":    base64.StdEncoding.EncodeToString(chunk),
		}); err != nil {
			return err
		}
		timer := time.NewTimer(20 * time.Millisecond)
		select {
		case <-ctx.Done():
			timer.Stop()
			return ctx.Err()
		case <-timer.C:
		}
	}
	return nil
}

// CommitAudio commits the current input audio buffer (force endpoint).
func (s *DuplexSession) CommitAudio(ctx context.Context) error {
	return s.send(ctx, map[string]any{
		"type":     "input_audio_buffer.commit",
		"event_id": uuid.NewString(),
	})
}

// TurnResult captures one user-audio turn observation for B14 V1/V3 probes.
type TurnResult struct {
	Transcript     string   `json:"transcript"`
	AssistantText  string   `json:"assistant_text"`
	EventTypes     []string `json:"event_types"`
	ASRStartedAtMS int64    `json:"asr_started_at_ms,omitempty"`
	ASRDoneAtMS    int64    `json:"asr_done_at_ms,omitempty"`
}

// SendUserPCMAndWait uploads PCM, commits, and collects ASR + assistant text events.
func (s *DuplexSession) SendUserPCMAndWait(ctx context.Context, pcm []byte, waitAfterCommit time.Duration) (TurnResult, error) {
	var out TurnResult
	if waitAfterCommit <= 0 {
		waitAfterCommit = 25 * time.Second
	}
	started := time.Now()
	if err := s.SendPCM(ctx, pcm); err != nil {
		return out, err
	}
	if err := s.CommitAudio(ctx); err != nil {
		return out, err
	}

	deadline := time.Now().Add(waitAfterCommit)
	var text strings.Builder
	for time.Now().Before(deadline) {
		readCtx, cancel := context.WithTimeout(ctx, time.Until(deadline))
		evt, err := s.recv(readCtx)
		cancel()
		if err != nil {
			if text.Len() > 0 || out.Transcript != "" {
				break
			}
			return out, err
		}
		out.EventTypes = append(out.EventTypes, evt.Type)
		switch evt.Type {
		case "conversation.item.input_audio_transcription.started":
			out.ASRStartedAtMS = time.Since(started).Milliseconds()
			if t := evt.Transcript; t != "" {
				out.Transcript = t
			}
		case "conversation.item.input_audio_transcription.delta":
			// Duplex often sends a running hypothesis in delta/transcript (replace, don't concat).
			if evt.Transcript != "" {
				out.Transcript = evt.Transcript
			} else if evt.Delta != "" {
				out.Transcript = evt.Delta
			}
		case "conversation.item.input_audio_transcription.completed":
			out.ASRDoneAtMS = time.Since(started).Milliseconds()
			if evt.Transcript != "" {
				out.Transcript = evt.Transcript
			} else if evt.Text != "" {
				out.Transcript = evt.Text
			}
		case "input_audio_buffer.committed":
			// Endpoint forced; keep reading for ASR completed / model response.
		case "response.output_text.delta":
			if evt.Delta != "" {
				text.WriteString(evt.Delta)
			}
		case "response.output_text.done":
			if evt.Text != "" {
				text.Reset()
				text.WriteString(evt.Text)
			}
		case "response.output_audio.done", "response.done":
			out.AssistantText = strings.TrimSpace(text.String())
			return out, nil
		case "error":
			return out, fmt.Errorf("duplex error during turn: %s", evt.Raw)
		}
	}
	out.AssistantText = strings.TrimSpace(text.String())
	return out, nil
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
	Type       string
	SessionID  string
	Delta      string
	Text       string
	Transcript string
	Raw        string
}

func (s *DuplexSession) recv(ctx context.Context) (duplexEvent, error) {
	_, data, err := s.conn.Read(ctx)
	if err != nil {
		return duplexEvent{}, err
	}
	var envelope struct {
		Type       string `json:"type"`
		Delta      string `json:"delta"`
		Text       string `json:"text"`
		Transcript string `json:"transcript"`
		Session    *struct {
			ID string `json:"id"`
		} `json:"session"`
	}
	_ = json.Unmarshal(data, &envelope)
	evt := duplexEvent{
		Type:       envelope.Type,
		Delta:      envelope.Delta,
		Text:       envelope.Text,
		Transcript: envelope.Transcript,
		Raw:        string(data),
	}
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
		"ok":                true,
		"provider":          "volc-duplex",
		"endpoint":          firstNonEmpty(cfg.Endpoint, defaultDuplexEndpoint),
		"session_id":        session.SessionID(),
		"log_id":            session.LogID(),
		"inject_channel":    "session.update",
		"inject_channel_ok": true,
		"elapsed_ms":        time.Since(started).Milliseconds(),
		"credential_mode":   "live",
		"notes": []string{
			"D2 PASS: duplex WSS + session.create + session.update",
			"Full T9 delay-gradient still needs audio turn + same-turn observation",
		},
	}, nil
}

// SmokeDuplexASR runs B14 D3/T2: upload fixture PCM and require ASR transcript (V1).
func SmokeDuplexASR(ctx context.Context, cfg DuplexConfig, wavPath string) (map[string]any, error) {
	started := time.Now()
	pcm, rate, err := LoadWAVPCM16LE(wavPath)
	if err != nil {
		return nil, err
	}
	if rate != 16000 {
		return nil, fmt.Errorf("fixture sample rate %d != 16000", rate)
	}

	cfg.Instructions = firstNonEmpty(cfg.Instructions,
		"你是 FluentWork B14 ASR smoke 助手。用一句中文简短回应用户。")
	session, err := OpenDuplex(ctx, cfg)
	if err != nil {
		return nil, err
	}
	defer session.Close(ctx)

	turn, err := session.SendUserPCMAndWait(ctx, pcm, 30*time.Second)
	if err != nil {
		return nil, err
	}
	transcript := strings.TrimSpace(turn.Transcript)
	v1OK := transcript != ""
	out := map[string]any{
		"ok":              v1OK,
		"provider":        "volc-duplex",
		"session_id":      session.SessionID(),
		"log_id":          session.LogID(),
		"v1_asr_text_ok":  v1OK,
		"transcript":      transcript,
		"assistant_text":  turn.AssistantText,
		"asr_started_ms":  turn.ASRStartedAtMS,
		"asr_done_ms":     turn.ASRDoneAtMS,
		"event_types":     turn.EventTypes,
		"pcm_bytes":       len(pcm),
		"elapsed_ms":      time.Since(started).Milliseconds(),
		"credential_mode": "live",
		"fixture":         wavPath,
	}
	if !v1OK {
		return out, fmt.Errorf("V1 FAIL: no ASR transcript in events %v", turn.EventTypes)
	}
	return out, nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
