package voicepoc

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/coder/websocket"
	"github.com/google/uuid"

	"github.com/FluentWork/fluentwork-backend/pkg/logx"
)

const (
	defaultDuplexEndpoint = "wss://openspeech.bytedance.com/api/v3/duplex/realtime/dialogue"
	defaultDuplexModel    = "1.2.6.0"
	defaultDuplexVoice    = "zh_female_vv_jupiter_bigtts"
)

// 20ms of 16 kHz mono s16le.
const pcm16kChunkBytes = 640

// DuplexConfig configures a Volcano realtime duplex (Seeduplex) session.
// Auth is API-Key only (X-Api-Key); no AppID / Access Token / ResourceId required.
type DuplexConfig struct {
	APIKey   string
	Endpoint string
	Model    string
	Voice    string
	Logger   *slog.Logger
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

	seg := logx.Begin(cfg.Logger, "voice.duplex.open",
		"module", "voicepoc.duplex",
		"provider", "volc-duplex",
		"endpoint", firstNonEmpty(cfg.Endpoint, defaultDuplexEndpoint),
		"stage", "transport",
	)
	var openErr error
	var endAttrs []any
	defer func() {
		seg.End(openErr, endAttrs...)
	}()

	header := http.Header{}
	header.Set("X-Api-Key", cfg.APIKey)
	header.Set("X-Api-Connect-Id", uuid.NewString())

	conn, resp, err := websocket.Dial(ctx, cfg.Endpoint, &websocket.DialOptions{HTTPHeader: header})
	if err != nil {
		if resp != nil {
			openErr = fmt.Errorf("duplex dial: %w (http=%d logid=%s)", err, resp.StatusCode, resp.Header.Get("X-Tt-Logid"))
			return nil, openErr
		}
		openErr = fmt.Errorf("duplex dial: %w", err)
		return nil, openErr
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
		openErr = err
		return nil, openErr
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
				openErr = fmt.Errorf("session.created missing session.id")
				return nil, openErr
			}
			endAttrs = []any{
				"session_id", s.sessionID,
				"log_id", s.logID,
				"model", s.cfg.Model,
				"voice", s.cfg.Voice,
			}
			return s, nil
		case "error":
			_ = s.Close(ctx)
			openErr = fmt.Errorf("duplex error after create: %s", evt.Raw)
			return nil, openErr
		default:
			// ignore stray events before session.created
		}
	}
}

// UpdateInstructions sends session.update — B14 V2 mid-session inject channel.
// Non-update events observed while waiting are returned so callers can keep ASR/text.
func (s *DuplexSession) UpdateInstructions(ctx context.Context, instructions string) ([]duplexEvent, error) {
	if s == nil || s.conn == nil {
		return nil, fmt.Errorf("duplex session is nil")
	}
	seg := logx.Begin(s.cfg.Logger, "voice.duplex.update_instructions",
		"module", "voicepoc.duplex",
		"provider", "volc-duplex",
		"session_id", s.sessionID,
		"log_id", s.logID,
		"stage", "orchestration",
	)
	var updateErr error
	var endAttrs []any
	defer func() {
		seg.End(updateErr, endAttrs...)
	}()

	if err := s.send(ctx, map[string]any{
		"type":     "session.update",
		"event_id": uuid.NewString(),
		"session":  s.sessionPayload(instructions),
	}); err != nil {
		updateErr = err
		return nil, updateErr
	}
	var skipped []duplexEvent
	deadline := time.Now().Add(8 * time.Second)
	for time.Now().Before(deadline) {
		readCtx, cancel := context.WithTimeout(ctx, time.Until(deadline))
		evt, err := s.recv(readCtx)
		cancel()
		if err != nil {
			updateErr = fmt.Errorf("wait session.updated: %w", err)
			return skipped, updateErr
		}
		switch evt.Type {
		case "session.updated":
			endAttrs = []any{
				"skipped_event_count", len(skipped),
				"instruction_len", len(strings.TrimSpace(instructions)),
			}
			return skipped, nil
		case "error":
			updateErr = fmt.Errorf("duplex error after update: %s", evt.Raw)
			return skipped, updateErr
		default:
			skipped = append(skipped, evt)
		}
	}
	updateErr = fmt.Errorf("timeout waiting for session.updated")
	return skipped, updateErr
}

// SendPCM streams 16 kHz mono s16le PCM as 20ms input_audio_buffer.append frames.
func (s *DuplexSession) SendPCM(ctx context.Context, pcm []byte) error {
	if s == nil || s.conn == nil {
		return fmt.Errorf("duplex session is nil")
	}
	seg := logx.Begin(s.cfg.Logger, "voice.duplex.send_pcm",
		"module", "voicepoc.duplex",
		"provider", "volc-duplex",
		"session_id", s.sessionID,
		"log_id", s.logID,
		"stage", "asr",
		"pcm_bytes", len(pcm),
	)
	var sendErr error
	defer func() {
		seg.End(sendErr,
			"chunk_count", chunkCount(len(pcm), pcm16kChunkBytes),
			"audio_sec", pcmAudioSeconds(len(pcm)),
		)
	}()
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
			sendErr = ctx.Err()
			return sendErr
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
	started := time.Now()
	if err := s.SendPCM(ctx, pcm); err != nil {
		return TurnResult{}, err
	}
	if err := s.CommitAudio(ctx); err != nil {
		return TurnResult{}, err
	}
	return s.collectTurn(ctx, started, nil, waitAfterCommit)
}

// SendUserPCMInjectAfterCommit streams PCM, commits, injects instructions, then waits for the reply.
// Models T3/T4: mid-session inject immediately after user endpoint (≈ VAD-stop + 0ms).
func (s *DuplexSession) SendUserPCMInjectAfterCommit(ctx context.Context, pcm []byte, inject string, waitAfterInject time.Duration) (TurnResult, time.Duration, error) {
	return s.sendUserPCMInject(ctx, pcm, inject, waitAfterInject, true)
}

// SendUserPCMInjectBeforeCommit streams PCM, waits for ASR start, injects, then commits.
// Closer to B7: inject while recognition is live, before forcing endpoint.
func (s *DuplexSession) SendUserPCMInjectBeforeCommit(ctx context.Context, pcm []byte, inject string, waitAfterInject time.Duration) (TurnResult, time.Duration, error) {
	return s.sendUserPCMInject(ctx, pcm, inject, waitAfterInject, false)
}

func (s *DuplexSession) sendUserPCMInject(ctx context.Context, pcm []byte, inject string, waitAfterInject time.Duration, injectAfterCommit bool) (TurnResult, time.Duration, error) {
	started := time.Now()
	if err := s.SendPCM(ctx, pcm); err != nil {
		return TurnResult{}, 0, err
	}

	var preload []duplexEvent
	if !injectAfterCommit {
		// Drain early ASR events buffered during upload, then inject before commit.
		drainCtx, cancel := context.WithTimeout(ctx, 3*time.Second)
		for {
			evt, err := s.recv(drainCtx)
			if err != nil {
				break
			}
			preload = append(preload, evt)
			if evt.Type == "conversation.item.input_audio_transcription.started" ||
				evt.Type == "conversation.item.input_audio_transcription.delta" ||
				evt.Type == "conversation.item.input_audio_transcription.completed" {
				break
			}
		}
		cancel()
	}

	var injectLatency time.Duration
	if !injectAfterCommit {
		t0 := time.Now()
		skipped, err := s.UpdateInstructions(ctx, inject)
		injectLatency = time.Since(t0)
		if err != nil {
			return TurnResult{}, injectLatency, err
		}
		preload = append(preload, skipped...)
		if err := s.CommitAudio(ctx); err != nil {
			return TurnResult{}, injectLatency, err
		}
	} else {
		if err := s.CommitAudio(ctx); err != nil {
			return TurnResult{}, 0, err
		}
		t0 := time.Now()
		skipped, err := s.UpdateInstructions(ctx, inject)
		injectLatency = time.Since(t0)
		if err != nil {
			return TurnResult{}, injectLatency, err
		}
		preload = append(preload, skipped...)
	}

	turn, err := s.collectTurn(ctx, started, preload, waitAfterInject)
	return turn, injectLatency, err
}

func (s *DuplexSession) collectTurn(ctx context.Context, started time.Time, preload []duplexEvent, wait time.Duration) (TurnResult, error) {
	var out TurnResult
	seg := logx.Begin(s.cfg.Logger, "voice.duplex.collect_turn",
		"module", "voicepoc.duplex",
		"provider", "volc-duplex",
		"session_id", s.sessionID,
		"log_id", s.logID,
		"stage", "tts",
	)
	var collectErr error
	var endAttrs []any
	defer func() {
		endAttrs = append(endAttrs,
			"event_count", len(out.EventTypes),
			"transcript_len", len(strings.TrimSpace(out.Transcript)),
			"assistant_text_len", len(strings.TrimSpace(out.AssistantText)),
			"asr_started_ms", out.ASRStartedAtMS,
			"asr_done_ms", out.ASRDoneAtMS,
		)
		seg.End(collectErr, endAttrs...)
	}()
	if wait <= 0 {
		wait = 25 * time.Second
	}
	var text strings.Builder
	seenUserProgress := false
	seenResponse := false
	apply := func(evt duplexEvent) bool {
		out.EventTypes = append(out.EventTypes, evt.Type)
		switch evt.Type {
		case "conversation.item.input_audio_transcription.started":
			seenUserProgress = true
			out.ASRStartedAtMS = time.Since(started).Milliseconds()
			if t := evt.Transcript; t != "" {
				out.Transcript = t
			}
		case "conversation.item.input_audio_transcription.delta":
			seenUserProgress = true
			if evt.Transcript != "" {
				out.Transcript = evt.Transcript
			} else if evt.Delta != "" {
				out.Transcript = evt.Delta
			}
		case "conversation.item.input_audio_transcription.completed":
			seenUserProgress = true
			out.ASRDoneAtMS = time.Since(started).Milliseconds()
			if evt.Transcript != "" {
				out.Transcript = evt.Transcript
			} else if evt.Text != "" {
				out.Transcript = evt.Text
			}
		case "input_audio_buffer.committed":
			seenUserProgress = true
		case "response.output_text.delta":
			seenResponse = true
			if evt.Delta != "" {
				text.WriteString(evt.Delta)
			}
		case "response.output_text.done":
			seenResponse = true
			if evt.Text != "" {
				text.Reset()
				text.WriteString(evt.Text)
			}
		case "response.output_audio.started":
			seenResponse = true
		case "response.output_audio.done", "response.done":
			// Ignore stale done from a previous turn until this turn has user+response progress.
			if !seenUserProgress || !seenResponse {
				return false
			}
			out.AssistantText = strings.TrimSpace(text.String())
			return true
		case "error":
			return true
		}
		return false
	}

	for _, evt := range preload {
		if evt.Type == "error" {
			out.AssistantText = strings.TrimSpace(text.String())
			collectErr = fmt.Errorf("duplex error during turn: %s", evt.Raw)
			return out, collectErr
		}
		if apply(evt) {
			if evt.Type == "error" {
				collectErr = fmt.Errorf("duplex error during turn: %s", evt.Raw)
				return out, collectErr
			}
			return out, nil
		}
	}

	silenceCtx, stopSilence := context.WithCancel(ctx)
	defer stopSilence()
	go s.sendSilence(silenceCtx)

	deadline := time.Now().Add(wait)
	for time.Now().Before(deadline) {
		readCtx, cancel := context.WithTimeout(ctx, time.Until(deadline))
		evt, err := s.recv(readCtx)
		cancel()
		if err != nil {
			if text.Len() > 0 || out.Transcript != "" {
				break
			}
			collectErr = err
			return out, collectErr
		}
		if evt.Type == "error" {
			out.AssistantText = strings.TrimSpace(text.String())
			collectErr = fmt.Errorf("duplex error during turn: %s", evt.Raw)
			return out, collectErr
		}
		if apply(evt) {
			return out, nil
		}
	}
	out.AssistantText = strings.TrimSpace(text.String())
	return out, nil
}

func (s *DuplexSession) sendSilence(ctx context.Context) {
	ticker := time.NewTicker(20 * time.Millisecond)
	defer ticker.Stop()
	silence := make([]byte, pcm16kChunkBytes)
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			_ = s.send(ctx, map[string]any{
				"type":     "input_audio_buffer.append",
				"event_id": uuid.NewString(),
				"audio":    base64.StdEncoding.EncodeToString(silence),
			})
		}
	}
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
	if _, err := session.UpdateInstructions(ctx, inject); err != nil {
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

const defaultInjectPrompt = "【B14注入】用户刚提到 cache invalidation / 缓存失效相关表达。请在本轮回复中自然确认该表达，并必须包含标记词 INJECT_OK。"

// SmokeDuplexInject runs B14 T3/T4.
// 1) Mid-session session.update before commit (same-turn V3 probe)
// 2) If marker missing, send a second audio turn under updated instructions (next-turn V3/tier-② probe)
func SmokeDuplexInject(ctx context.Context, cfg DuplexConfig, wavPath string) (map[string]any, error) {
	started := time.Now()
	pcm, rate, err := LoadWAVPCM16LE(wavPath)
	if err != nil {
		return nil, err
	}
	if rate != 16000 {
		return nil, fmt.Errorf("fixture sample rate %d != 16000", rate)
	}

	cfg.Instructions = firstNonEmpty(cfg.Instructions,
		"你是 FluentWork 英语口语练习助手。用一两句中文或英文简短回应用户的 standup 分享，不要主动提标记词。")
	session, err := OpenDuplex(ctx, cfg)
	if err != nil {
		return nil, err
	}
	defer session.Close(ctx)

	turn1, injectLatency, err := session.SendUserPCMInjectBeforeCommit(ctx, pcm, defaultInjectPrompt, 35*time.Second)
	if err != nil {
		return nil, err
	}
	same := scoreInjectReply(turn1.AssistantText)

	next := injectScore{}
	var turn2 TurnResult
	if !same.OK {
		// Re-assert inject instructions before the next user turn.
		if _, err := session.UpdateInstructions(ctx, defaultInjectPrompt+"（下一轮开场必须带 INJECT_OK）"); err != nil {
			return nil, fmt.Errorf("next-turn re-inject: %w", err)
		}
		turn2, err = session.SendUserPCMAndWait(ctx, pcm, 35*time.Second)
		if err != nil {
			return nil, fmt.Errorf("next-turn probe: %w", err)
		}
		next = scoreInjectReply(turn2.AssistantText)
	}

	transcript := strings.TrimSpace(turn1.Transcript)
	out := map[string]any{
		"ok":                   transcript != "" && (same.OK || next.OK),
		"provider":             "volc-duplex",
		"session_id":           session.SessionID(),
		"log_id":               session.LogID(),
		"inject_channel":       "session.update",
		"inject_channel_ok":    true,
		"inject_latency_ms":    injectLatency.Milliseconds(),
		"v1_asr_text_ok":       transcript != "",
		"v3_same_turn_ok":      same.OK,
		"v3_next_turn_ok":      next.OK,
		"v3_inject_effect_ok":  same.OK || next.OK,
		"same_turn":            same,
		"next_turn":            next,
		"transcript":           transcript,
		"assistant_text":       strings.TrimSpace(turn1.AssistantText),
		"assistant_text_turn2": strings.TrimSpace(turn2.AssistantText),
		"asr_started_ms":       turn1.ASRStartedAtMS,
		"asr_done_ms":          turn1.ASRDoneAtMS,
		"event_types":          turn1.EventTypes,
		"event_types_turn2":    turn2.EventTypes,
		"pcm_bytes":            len(pcm),
		"elapsed_ms":           time.Since(started).Milliseconds(),
		"credential_mode":      "live",
		"fixture":              wavPath,
		"b7_tier_hint":         tierHint(same.OK, next.OK),
		"notes": []string{
			"T3: session.update ack = inject channel exists (V2)",
			"T4 same-turn: update before commit; control showed create-time instructions CAN force INJECT_OK",
			"If only next-turn hits: prefer B7 tier ② (next-turn open confirm), pending T9 window",
			"Single-trial ≠ V5 10-run ratio",
		},
	}
	if transcript == "" {
		return out, fmt.Errorf("T3/T4 FAIL: no ASR transcript; events=%v", turn1.EventTypes)
	}
	if !same.OK && !next.OK {
		return out, fmt.Errorf("T3/T4 FAIL: neither same-turn nor next-turn showed inject effect; turn1=%q turn2=%q",
			turn1.AssistantText, turn2.AssistantText)
	}
	return out, nil
}

type injectScore struct {
	OK         bool `json:"ok"`
	HitMarker  bool `json:"hit_marker"`
	HitTopic   bool `json:"hit_topic"`
	HitConfirm bool `json:"hit_confirm"`
}

func scoreInjectReply(assistant string) injectScore {
	assistant = strings.TrimSpace(assistant)
	s := injectScore{
		HitMarker: containsFold(assistant, "INJECT_OK"),
		HitTopic: containsFold(assistant, "cache") || containsFold(assistant, "invalidat") ||
			containsFold(assistant, "缓存") || containsFold(assistant, "失效"),
		HitConfirm: containsFold(assistant, "确认") || containsFold(assistant, "提到") ||
			containsFold(assistant, "用到") || containsFold(assistant, "不错") ||
			containsFold(assistant, "很好") || containsFold(assistant, "看到你") ||
			containsFold(assistant, "got it") || containsFold(assistant, "covered") ||
			containsFold(assistant, "key point"),
	}
	// For B14 evidence we require the explicit inject marker. The fixture itself
	// already talks about cache invalidation, so topic+confirm alone is not
	// strong enough to prove the mid-session update actually took effect.
	s.OK = assistant != "" && s.HitMarker
	return s
}

func tierHint(sameTurn, nextTurn bool) string {
	switch {
	case sameTurn:
		return "candidate ① same-turn (needs T9 window ≥800ms to freeze)"
	case nextTurn:
		return "candidate ② next-turn open confirm (same-turn session.update ineffective in this trial)"
	default:
		return "candidate ③ badge only / need alternate inject API"
	}
}

func containsFold(s, substr string) bool {
	return strings.Contains(strings.ToLower(s), strings.ToLower(substr))
}

func chunkCount(total, size int) int {
	if total <= 0 || size <= 0 {
		return 0
	}
	return (total + size - 1) / size
}

func pcmAudioSeconds(bytes int) int {
	if bytes <= 0 {
		return 0
	}
	return bytes / (16000 * 2)
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
