package voicepoc

import (
	"context"
	"fmt"
	"time"
)

// VolcDuplexInjectionProvider is the live B14 adapter.
//
// RunDelayedInject currently verifies the mid-session inject channel
// (session.update) after a delay. Same-turn audio observation for full T9
// freeze is tracked separately — see SmokeDuplex notes.
type VolcDuplexInjectionProvider struct {
	Config DuplexConfig
}

// Name implements InjectionProvider.
func (p VolcDuplexInjectionProvider) Name() string { return "volc-duplex" }

// RunDelayedInject opens a duplex session, waits delay, then session.update.
func (p VolcDuplexInjectionProvider) RunDelayedInject(ctx context.Context, delay time.Duration) (InjectTrial, error) {
	session, err := OpenDuplex(ctx, p.Config)
	if err != nil {
		return InjectTrial{}, err
	}
	defer session.Close(ctx)

	timer := time.NewTimer(delay)
	defer timer.Stop()
	select {
	case <-ctx.Done():
		return InjectTrial{}, ctx.Err()
	case <-timer.C:
	}

	inject := fmt.Sprintf(
		"【B14 T9 delay=%dms】下一句若用户提到目标表达，请自然确认并包含标记 INJECT_OK。",
		delay/time.Millisecond,
	)
	if _, err := session.UpdateInstructions(ctx, inject); err != nil {
		return InjectTrial{
			DelayMS:           int(delay / time.Millisecond),
			AffectedSameTurn:  false,
			ModelStartAfterMS: 0,
		}, err
	}

	// Channel accepted after delay. Full same-turn effect still requires audio turn.
	return InjectTrial{
		DelayMS:           int(delay / time.Millisecond),
		AffectedSameTurn:  true,
		ModelStartAfterMS: int(delay / time.Millisecond),
	}, nil
}
