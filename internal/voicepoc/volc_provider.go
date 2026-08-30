package voicepoc

import (
	"context"
	"fmt"
	"strings"
	"time"
)

// VolcDuplexInjectionProvider is the live B14 T9 adapter.
//
// Each trial: upload fixture PCM → commit (≈ VAD-stop) → wait delay →
// session.update → score same-turn reply; if miss, one next-turn probe.
type VolcDuplexInjectionProvider struct {
	Config  DuplexConfig
	WavPath string
	// SkipNextTurnProbe saves cost when only measuring same-turn window.
	SkipNextTurnProbe bool
}

// Name implements InjectionProvider.
func (p VolcDuplexInjectionProvider) Name() string { return "volc-duplex" }

// RunDelayedInject implements InjectionProvider for live T9.
func (p VolcDuplexInjectionProvider) RunDelayedInject(ctx context.Context, delay time.Duration) (InjectTrial, error) {
	if strings.TrimSpace(p.WavPath) == "" {
		return InjectTrial{}, fmt.Errorf("VolcDuplexInjectionProvider.WavPath is required for live T9")
	}
	pcm, rate, err := LoadWAVPCM16LE(p.WavPath)
	if err != nil {
		return InjectTrial{}, err
	}
	if rate != 16000 {
		return InjectTrial{}, fmt.Errorf("fixture sample rate %d != 16000", rate)
	}

	cfg := p.Config
	cfg.Instructions = firstNonEmpty(cfg.Instructions,
		"你是 FluentWork 英语口语练习助手。用一两句中文或英文简短回应用户，不要主动提标记词。")

	session, err := OpenDuplex(ctx, cfg)
	if err != nil {
		return InjectTrial{}, err
	}
	defer session.Close(ctx)

	commitAt := time.Now()
	if err := session.SendPCM(ctx, pcm); err != nil {
		return InjectTrial{}, err
	}
	if err := session.CommitAudio(ctx); err != nil {
		return InjectTrial{}, err
	}

	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return InjectTrial{}, ctx.Err()
	case <-timer.C:
	}

	inject := fmt.Sprintf(
		"%s（T9 delay=%dms；本轮回复必须含 INJECT_OK）",
		defaultInjectPrompt,
		delay/time.Millisecond,
	)
	skipped, err := session.UpdateInstructions(ctx, inject)
	if err != nil {
		return InjectTrial{DelayMS: int(delay / time.Millisecond)}, err
	}

	turn1, err := session.collectTurn(ctx, commitAt, skipped, 35*time.Second)
	if err != nil {
		return InjectTrial{DelayMS: int(delay / time.Millisecond)}, err
	}
	same := scoreInjectReply(turn1.AssistantText)
	modelStart := int(turn1.ASRDoneAtMS)
	if modelStart == 0 {
		modelStart = int(time.Since(commitAt) / time.Millisecond)
	}

	trial := InjectTrial{
		DelayMS:           int(delay / time.Millisecond),
		AffectedSameTurn:  same.OK,
		ModelStartAfterMS: modelStart,
		SameTurnText:      strings.TrimSpace(turn1.AssistantText),
	}
	if same.OK || p.SkipNextTurnProbe {
		return trial, nil
	}

	if _, err := session.UpdateInstructions(ctx, inject+"（下一轮开场必须带 INJECT_OK）"); err != nil {
		return trial, fmt.Errorf("next-turn re-inject: %w", err)
	}
	turn2, err := session.SendUserPCMAndWait(ctx, pcm, 35*time.Second)
	if err != nil {
		return trial, fmt.Errorf("next-turn probe: %w", err)
	}
	next := scoreInjectReply(turn2.AssistantText)
	trial.AffectedNextTurn = next.OK
	trial.NextTurnText = strings.TrimSpace(turn2.AssistantText)
	return trial, nil
}

// SmokeDuplexT9 runs a cost-controlled live T9 gradient and returns the window report.
func SmokeDuplexT9(ctx context.Context, cfg DuplexConfig, wavPath string, delays []time.Duration, trialsPerDelay int) (WindowReport, error) {
	if trialsPerDelay <= 0 {
		trialsPerDelay = 1
	}
	provider := VolcDuplexInjectionProvider{Config: cfg, WavPath: wavPath}
	return RunT9(ctx, provider, delays, trialsPerDelay)
}
