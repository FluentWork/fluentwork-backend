package voicepoc

import (
	"context"
	"time"
)

// MockInjectionProvider simulates a vendor that keeps an injectable window until
// modelStartAfter. Delays at or before that threshold affect the same turn.
type MockInjectionProvider struct {
	// ModelStartAfter is the delay after VAD-stop when the model begins generating.
	ModelStartAfter time.Duration
}

// Name implements InjectionProvider.
func (m MockInjectionProvider) Name() string { return "mock" }

// RunDelayedInject implements InjectionProvider.
func (m MockInjectionProvider) RunDelayedInject(_ context.Context, delay time.Duration) (InjectTrial, error) {
	startAfter := m.ModelStartAfter
	if startAfter <= 0 {
		startAfter = 900 * time.Millisecond
	}
	delayMS := int(delay / time.Millisecond)
	startMS := int(startAfter / time.Millisecond)
	return InjectTrial{
		DelayMS:           delayMS,
		AffectedSameTurn:  delay <= startAfter,
		ModelStartAfterMS: startMS,
	}, nil
}

// ClosedWindowProvider never lets injection affect the same turn (forces tier ③).
type ClosedWindowProvider struct{}

func (ClosedWindowProvider) Name() string { return "mock-closed" }

func (ClosedWindowProvider) RunDelayedInject(_ context.Context, delay time.Duration) (InjectTrial, error) {
	return InjectTrial{
		DelayMS:           int(delay / time.Millisecond),
		AffectedSameTurn:  false,
		ModelStartAfterMS: 0,
	}, nil
}
