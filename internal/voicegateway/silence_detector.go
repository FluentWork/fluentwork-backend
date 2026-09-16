package voicegateway

import (
	"sync"
	"time"
)

// Default rescue thresholds (B8 / PRD V1.6 §8.2): one rung of the ladder every
// three seconds of silence that the user let pass.
const (
	DefaultRescueLevel1After = 3 * time.Second // skeleton: "I think the main risk is..."
	DefaultRescueLevel2After = 6 * time.Second // hint: "先说结论，再说原因"
	DefaultRescueLevel3After = 9 * time.Second // complete example sentence
)

// RescueThresholds lets a caller move the three rungs. Production uses the
// defaults; tests shorten them so a window measured in milliseconds still
// ticks, and a future A/B on the 3s trigger (docs/78 §9.2) can change them
// without touching the detector.
type RescueThresholds struct {
	Level1 time.Duration
	Level2 time.Duration
	Level3 time.Duration
}

// withDefaults fills unset rungs and forces them non-decreasing.
//
// Each rung is defaulted on its own before the ordering pass. Clamping alone is
// not enough: a zero Level2 would be raised to Level1 rather than to 6s, which
// collapses the ladder so that a caller who only meant to shorten the first rung
// gets all three at the same instant.
//
// A caller that passes Level1=5s, Level2=1s is asking for a ladder that climbs
// backwards; clamping keeps the invariant "each rung is at least as late as the
// one below it" true rather than leaving it to the caller.
func (t RescueThresholds) withDefaults() RescueThresholds {
	if t.Level1 <= 0 {
		t.Level1 = DefaultRescueLevel1After
	}
	if t.Level2 <= 0 {
		t.Level2 = DefaultRescueLevel2After
	}
	if t.Level3 <= 0 {
		t.Level3 = DefaultRescueLevel3After
	}
	if t.Level2 < t.Level1 {
		t.Level2 = t.Level1
	}
	if t.Level3 < t.Level2 {
		t.Level3 = t.Level2
	}
	return t
}

// SilenceDetector decides when a silent user has earned the next rung of the
// B8 rescue ladder.
//
// # What it measures
//
// One window: the stretch of time during which the floor is the user's — it
// opens when the AI stops speaking and closes when the user's turn ends. Within
// a window, each threshold fires at most once, so a user who says nothing at
// all hears skeleton → hint → example at 3s / 6s / 9s and then nothing more.
//
// The window deliberately does **not** open on user.speech.start. Opening it
// there is what the design doc's flow diagram (docs/78 §5.2) shows, but it
// leaves the case the feature exists for uncovered: a user who understood
// nothing says nothing, never sends user.speech.start, and would never be
// rescued. Opening on ai.tts.end covers that case and every case below it.
//
// # Why user.speech.start does not restart the clock
//
// It resets the *ladder* — a user who opens their mouth again gets a fresh
// three rungs — but it does not move the window. Moving it would let a
// half-utterance ("I think") buy another three seconds of silence, which is
// exactly the behaviour docs/78 §5.3 case 1 rules out.
//
// It is safe for concurrent use.
type SilenceDetector struct {
	mu sync.Mutex
	// thresholds is fixed at construction, so it needs no lock of its own.
	thresholds RescueThresholds
	// windowStart is when the floor passed to the user. Zero means no window is
	// open and no rescue can fire.
	windowStart time.Time
	// userSpeaking suspends rescue while the user holds the floor mid-utterance.
	userSpeaking bool
	// rescueLevel is the highest rung already fired in this window.
	rescueLevel int
}

// NewSilenceDetector creates a detector with the PRD's 3s / 6s / 9s thresholds.
func NewSilenceDetector() *SilenceDetector {
	return NewSilenceDetectorWithThresholds(RescueThresholds{})
}

// NewSilenceDetectorWithThresholds creates a detector with caller-supplied
// thresholds. Zero-valued or out-of-order rungs fall back to the defaults (see
// RescueThresholds.withDefaults).
func NewSilenceDetectorWithThresholds(thresholds RescueThresholds) *SilenceDetector {
	return &SilenceDetector{thresholds: thresholds.withDefaults()}
}

// Thresholds returns the rung spacing this detector was built with. The handler
// uses it to clone a per-session detector from the wired template without
// carrying the template's window along.
func (d *SilenceDetector) Thresholds() RescueThresholds {
	return d.thresholds
}

// OnAISpeechEnd opens a silence window: the AI just finished, so both the floor
// and the clock are the user's. Called for every AI turn end.
func (d *SilenceDetector) OnAISpeechEnd(now time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.windowStart = now
	d.rescueLevel = 0
	d.userSpeaking = false
}

// OnUserSpeechStart marks the user as speaking and resets the ladder.
//
// The window is left where it is on purpose — see the type comment — so that an
// utterance which turns out to be incomplete keeps counting from the moment the
// AI stopped talking.
func (d *SilenceDetector) OnUserSpeechStart(_ time.Time) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.userSpeaking = true
	d.rescueLevel = 0
}

// OnUserSpeechEnd closes the user's attempt.
//
// complete=true means the user produced a full utterance: they took the floor,
// so the window closes and no rung can fire until the AI hands it back. It must
// close rather than restart — restarting would arm the ladder against a user
// who is merely waiting for the AI to answer them.
//
// complete=false means the utterance was incomplete (or nothing we could
// judge). The user has not really taken the floor, so the window stays open and
// the ladder keeps climbing from where it was.
func (d *SilenceDetector) OnUserSpeechEnd(_ time.Time, complete bool) {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.userSpeaking = false
	if complete {
		d.windowStart = time.Time{}
		d.rescueLevel = 0
	}
}

// CheckSilence returns the next rung to fire, if any.
//
// It returns the **lowest** rung whose threshold has elapsed and which has not
// fired yet, never the highest. A poller that arrives late — a stalled ticker, a
// process that was paused — then still walks the ladder in order instead of
// skipping straight to the full example, and the "one rung every three seconds"
// spacing the PRD promises holds for any caller polling faster than the
// thresholds. A caller polling slower than a whole rung's spacing will see two
// rungs issued back to back; the gateway's 500ms tick (defaultRescueTick) is far
// inside that, so it never does.
//
// Each returned rung is consumed: it will not be returned again in this window.
func (d *SilenceDetector) CheckSilence(now time.Time) (shouldRescue bool, level int) {
	d.mu.Lock()
	defer d.mu.Unlock()

	if d.windowStart.IsZero() || d.userSpeaking {
		return false, 0
	}

	elapsed := now.Sub(d.windowStart)
	switch {
	case d.rescueLevel < 1 && elapsed >= d.thresholds.Level1:
		d.rescueLevel = 1
		return true, 1
	case d.rescueLevel < 2 && elapsed >= d.thresholds.Level2:
		d.rescueLevel = 2
		return true, 2
	case d.rescueLevel < 3 && elapsed >= d.thresholds.Level3:
		d.rescueLevel = 3
		return true, 3
	default:
		return false, 0
	}
}

// Reset clears the detector. Called when the session ends or a turn is aborted
// outright, so a ladder armed in one session cannot fire in the next.
func (d *SilenceDetector) Reset() {
	d.mu.Lock()
	defer d.mu.Unlock()
	d.windowStart = time.Time{}
	d.userSpeaking = false
	d.rescueLevel = 0
}

// GetState returns the current window for debugging and logging:
// when it opened, the highest rung fired in it, and how long it has been open.
func (d *SilenceDetector) GetState(now time.Time) (windowStart time.Time, currentLevel int, elapsed time.Duration) {
	d.mu.Lock()
	defer d.mu.Unlock()

	windowStart = d.windowStart
	currentLevel = d.rescueLevel
	if !d.windowStart.IsZero() {
		elapsed = now.Sub(d.windowStart)
	}
	return
}
