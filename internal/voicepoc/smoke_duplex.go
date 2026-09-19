package voicepoc

import (
	"context"
	"fmt"
	"strings"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/voiceduplex"
)

// Smoke probes for the Volc duplex transport.
//
// They live here, in the PoC package, rather than beside the transport they
// exercise. Splitting them out is what made `voiceduplex` readable as "the thing
// production runs": a `poc` package that also contained the live voice transport
// could not answer "is this on the production path?" from its name, and that is
// exactly the question a reader arrives with (docs/94_ F7).

// SmokeDuplex runs B14 D2: connect → session.create → session.update → close.
// Proves API-Key auth and mid-session inject channel (V2).
func SmokeDuplex(ctx context.Context, cfg voiceduplex.DuplexConfig) (map[string]any, error) {
	started := time.Now()
	session, err := voiceduplex.OpenDuplex(ctx, cfg)
	if err != nil {
		return nil, err
	}
	defer func() { _ = session.Close(ctx) }()

	inject := "【B14注入探针】请在后续回复中自然确认用户提到的目标表达；标记词 INJECT_OK。"
	if _, err := session.UpdateInstructions(ctx, inject); err != nil {
		return nil, err
	}

	return map[string]any{
		"ok":                true,
		"provider":          "volc-duplex",
		"endpoint":          voiceduplex.FirstNonEmpty(cfg.Endpoint, voiceduplex.DefaultDuplexEndpoint),
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
func SmokeDuplexASR(ctx context.Context, cfg voiceduplex.DuplexConfig, wavPath string) (map[string]any, error) {
	started := time.Now()
	pcm, rate, err := LoadWAVPCM16LE(wavPath)
	if err != nil {
		return nil, err
	}
	if rate != 16000 {
		return nil, fmt.Errorf("fixture sample rate %d != 16000", rate)
	}

	cfg.Instructions = voiceduplex.FirstNonEmpty(cfg.Instructions,
		"你是 FluentWork B14 ASR smoke 助手。用一句中文简短回应用户。")
	session, err := voiceduplex.OpenDuplex(ctx, cfg)
	if err != nil {
		return nil, err
	}
	defer func() { _ = session.Close(ctx) }()

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
func SmokeDuplexInject(ctx context.Context, cfg voiceduplex.DuplexConfig, wavPath string) (map[string]any, error) {
	started := time.Now()
	pcm, rate, err := LoadWAVPCM16LE(wavPath)
	if err != nil {
		return nil, err
	}
	if rate != 16000 {
		return nil, fmt.Errorf("fixture sample rate %d != 16000", rate)
	}

	cfg.Instructions = voiceduplex.FirstNonEmpty(cfg.Instructions,
		"你是 FluentWork 英语口语练习助手。用一两句中文或英文简短回应用户的 standup 分享，不要主动提标记词。")
	session, err := voiceduplex.OpenDuplex(ctx, cfg)
	if err != nil {
		return nil, err
	}
	defer func() { _ = session.Close(ctx) }()

	turn1, injectLatency, err := session.SendUserPCMInjectBeforeCommit(ctx, pcm, defaultInjectPrompt, 35*time.Second)
	if err != nil {
		return nil, err
	}
	same := scoreInjectReply(turn1.AssistantText)

	next := injectScore{}
	var turn2 voiceduplex.TurnResult
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
