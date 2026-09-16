package voicegateway

import (
	"sync"
	"testing"
	"time"
)

// The detector's tests drive time explicitly rather than sleeping: every method
// that cares about "now" takes it as an argument, so a 9-second ladder is
// asserted in microseconds and cannot go flaky on a loaded CI box.

func TestSilenceDetector_NoTrigger_BeforeAnyAIUtterance(t *testing.T) {
	detector := NewSilenceDetector()
	now := time.Now()

	// A fresh session has no window open: the AI has not handed the floor over
	// and the user has done nothing to be rescued from.
	if should, level := detector.CheckSilence(now); should || level != 0 {
		t.Fatalf("fresh detector triggered: should=%v level=%d", should, level)
	}
	if should, _ := detector.CheckSilence(now.Add(30 * time.Second)); should {
		t.Error("detector triggered without an AI turn ever ending")
	}
}

// The case the feature exists for: a user who understood nothing says nothing at
// all, so no user.speech.start ever arrives. Anchoring the window on
// user.speech.start would leave this user unrescued forever.
func TestSilenceDetector_FiresWithoutAnyUserSpeech(t *testing.T) {
	detector := NewSilenceDetector()
	start := time.Now()

	detector.OnAISpeechEnd(start)

	if should, _ := detector.CheckSilence(start.Add(2900 * time.Millisecond)); should {
		t.Error("fired before the 3s threshold")
	}
	should, level := detector.CheckSilence(start.Add(3 * time.Second))
	if !should || level != 1 {
		t.Fatalf("at 3s: should=%v level=%d, want true/1", should, level)
	}
	should, level = detector.CheckSilence(start.Add(6 * time.Second))
	if !should || level != 2 {
		t.Fatalf("at 6s: should=%v level=%d, want true/2", should, level)
	}
	should, level = detector.CheckSilence(start.Add(9 * time.Second))
	if !should || level != 3 {
		t.Fatalf("at 9s: should=%v level=%d, want true/3", should, level)
	}
	// Every rung fired once; the ladder is spent and the user is left alone
	// until B15's turn timeout ends the session.
	if should, _ := detector.CheckSilence(start.Add(60 * time.Second)); should {
		t.Error("ladder fired again after all three rungs were spent")
	}
}

// A caller that arrives late must still walk the ladder in order rather than
// jumping to the full example.
func TestSilenceDetector_LatePollDoesNotSkipRungs(t *testing.T) {
	detector := NewSilenceDetector()
	start := time.Now()

	detector.OnAISpeechEnd(start)

	// First poll only after 10 seconds: all three thresholds have elapsed.
	for want := 1; want <= 3; want++ {
		should, level := detector.CheckSilence(start.Add(10 * time.Second))
		if !should || level != want {
			t.Fatalf("late poll #%d: should=%v level=%d, want true/%d", want, should, level, want)
		}
	}
	if should, _ := detector.CheckSilence(start.Add(10 * time.Second)); should {
		t.Error("ladder kept firing past level 3")
	}
}

func TestSilenceDetector_ThresholdSpacingIsThreeSeconds(t *testing.T) {
	detector := NewSilenceDetector()
	start := time.Now()
	detector.OnAISpeechEnd(start)

	// Poll the way the gateway's 500ms ticker would, and record when each rung
	// first appears. Walking the clock rather than jumping to each threshold is
	// what makes this a test of the spacing instead of a restatement of it.
	var fired []time.Duration
	for offset := time.Duration(0); offset <= 10*time.Second; offset += 100 * time.Millisecond {
		if should, level := detector.CheckSilence(start.Add(offset)); should {
			if level != len(fired)+1 {
				t.Fatalf("rungs out of order: got level %d as rung %d", level, len(fired)+1)
			}
			fired = append(fired, offset)
		}
	}

	if len(fired) != 3 {
		t.Fatalf("fired %d rungs (%v), want 3", len(fired), fired)
	}
	for i, got := range fired {
		want := time.Duration(i+1) * DefaultRescueLevel1After
		if got != want {
			t.Errorf("rung %d fired at %v, want %v", i+1, got, want)
		}
	}
}

func TestSilenceDetector_UserSpeakingSuspendsRescue(t *testing.T) {
	detector := NewSilenceDetector()
	start := time.Now()

	detector.OnAISpeechEnd(start)
	detector.OnUserSpeechStart(start.Add(2 * time.Second))

	// The user is mid-sentence: a skeleton prompt would talk over them.
	if should, _ := detector.CheckSilence(start.Add(4 * time.Second)); should {
		t.Error("fired while the user was speaking")
	}
	if should, _ := detector.CheckSilence(start.Add(20 * time.Second)); should {
		t.Error("fired while the user was speaking, long after the threshold")
	}
}

func TestSilenceDetector_CompleteUtteranceClosesTheWindow(t *testing.T) {
	detector := NewSilenceDetector()
	start := time.Now()

	detector.OnAISpeechEnd(start)
	detector.OnUserSpeechStart(start.Add(time.Second))
	detector.OnUserSpeechEnd(start.Add(2*time.Second), true)

	// The user took the floor, so the AI now owes them a reply. No rung may
	// fire while the AI is preparing or playing it.
	if should, _ := detector.CheckSilence(start.Add(30 * time.Second)); should {
		t.Error("fired after the user answered completely")
	}

	// The next AI turn hands the floor back and the ladder starts over.
	detector.OnAISpeechEnd(start.Add(5 * time.Second))
	should, level := detector.CheckSilence(start.Add(8 * time.Second))
	if !should || level != 1 {
		t.Fatalf("new window: should=%v level=%d, want true/1", should, level)
	}
}

// docs/78 §5.3 case 1: a half-utterance must not buy another three seconds.
// The user said "I think" at 2.5s and stopped; the skeleton is due at 3s from
// the AI's turn end, not from the user's.
func TestSilenceDetector_IncompleteUtteranceKeepsTheOriginalWindow(t *testing.T) {
	detector := NewSilenceDetector()
	start := time.Now()

	detector.OnAISpeechEnd(start)
	detector.OnUserSpeechStart(start.Add(2400 * time.Millisecond))
	detector.OnUserSpeechEnd(start.Add(2500*time.Millisecond), false)

	if should, _ := detector.CheckSilence(start.Add(2900 * time.Millisecond)); should {
		t.Error("fired before the original 3s threshold")
	}
	should, level := detector.CheckSilence(start.Add(3 * time.Second))
	if !should || level != 1 {
		t.Fatalf("at 3s from the AI turn: should=%v level=%d, want true/1", should, level)
	}
}

func TestSilenceDetector_UserSpeechStartResetsTheLadder(t *testing.T) {
	detector := NewSilenceDetector()
	start := time.Now()

	detector.OnAISpeechEnd(start)
	if should, level := detector.CheckSilence(start.Add(3 * time.Second)); !should || level != 1 {
		t.Fatalf("setup: expected level 1, got should=%v level=%d", should, level)
	}

	// The user opens their mouth again: a second attempt gets a fresh ladder.
	detector.OnUserSpeechStart(start.Add(3100 * time.Millisecond))

	_, level, _ := detector.GetState(start.Add(3200 * time.Millisecond))
	if level != 0 {
		t.Errorf("ladder level = %d after a new user utterance, want 0", level)
	}

	detector.OnUserSpeechEnd(start.Add(3200*time.Millisecond), false)
	should, level := detector.CheckSilence(start.Add(3300 * time.Millisecond))
	if !should || level != 1 {
		t.Fatalf("after restart: should=%v level=%d, want true/1", should, level)
	}
}

func TestSilenceDetector_Reset_Clears_State(t *testing.T) {
	detector := NewSilenceDetector()
	start := time.Now()

	detector.OnAISpeechEnd(start)
	detector.CheckSilence(start.Add(3 * time.Second))

	detector.Reset()

	windowStart, level, elapsed := detector.GetState(start.Add(10 * time.Second))
	if !windowStart.IsZero() {
		t.Error("Expected the window to be closed after reset")
	}
	if level != 0 {
		t.Errorf("Expected level=0 after reset, got %d", level)
	}
	if elapsed != 0 {
		t.Errorf("Expected elapsed=0, got %v", elapsed)
	}
	if should, _ := detector.CheckSilence(start.Add(10 * time.Second)); should {
		t.Error("Should not trigger after reset")
	}
}

func TestSilenceDetector_GetState(t *testing.T) {
	detector := NewSilenceDetector()
	start := time.Now()

	detector.OnAISpeechEnd(start)
	detector.CheckSilence(start.Add(3 * time.Second))

	windowStart, level, elapsed := detector.GetState(start.Add(5 * time.Second))
	if !windowStart.Equal(start) {
		t.Errorf("Expected windowStart=%v, got %v", start, windowStart)
	}
	if level != 1 {
		t.Errorf("Expected level=1, got %d", level)
	}
	if elapsed != 5*time.Second {
		t.Errorf("Expected elapsed=5s, got %v", elapsed)
	}
}

func TestSilenceDetector_CustomThresholds(t *testing.T) {
	detector := NewSilenceDetectorWithThresholds(RescueThresholds{
		Level1: 50 * time.Millisecond,
		Level2: 100 * time.Millisecond,
		Level3: 150 * time.Millisecond,
	})
	start := time.Now()
	detector.OnAISpeechEnd(start)

	if should, _ := detector.CheckSilence(start.Add(49 * time.Millisecond)); should {
		t.Error("fired before the custom level-1 threshold")
	}
	if should, level := detector.CheckSilence(start.Add(50 * time.Millisecond)); !should || level != 1 {
		t.Errorf("custom level 1: should=%v level=%d", should, level)
	}
	if should, level := detector.CheckSilence(start.Add(100 * time.Millisecond)); !should || level != 2 {
		t.Errorf("custom level 2: should=%v level=%d", should, level)
	}
	if should, level := detector.CheckSilence(start.Add(150 * time.Millisecond)); !should || level != 3 {
		t.Errorf("custom level 3: should=%v level=%d", should, level)
	}
}

// A backwards ladder (level 2 due before level 1) is a caller mistake that would
// make level 2 unreachable, since CheckSilence walks upward. Clamping keeps the
// rungs ordered instead of silently dropping one.
func TestSilenceDetector_ThresholdsAreClampedNonDecreasing(t *testing.T) {
	detector := NewSilenceDetectorWithThresholds(RescueThresholds{
		Level1: 5 * time.Second,
		Level2: time.Second,
		Level3: 2 * time.Second,
	})

	got := detector.Thresholds()
	want := RescueThresholds{Level1: 5 * time.Second, Level2: 5 * time.Second, Level3: 5 * time.Second}
	if got != want {
		t.Fatalf("clamped thresholds = %+v, want %+v", got, want)
	}
}

func TestSilenceDetector_ZeroThresholdsUseDefaults(t *testing.T) {
	detector := NewSilenceDetectorWithThresholds(RescueThresholds{})
	want := RescueThresholds{
		Level1: DefaultRescueLevel1After,
		Level2: DefaultRescueLevel2After,
		Level3: DefaultRescueLevel3After,
	}
	if got := detector.Thresholds(); got != want {
		t.Fatalf("default thresholds = %+v, want %+v", got, want)
	}
}

// The detector is driven from a poller goroutine while the read loop feeds it
// AI turn ends and user speech, so the race detector has to be able to prove it
// safe. Run with -race for this one to mean anything.
func TestSilenceDetector_Concurrent_Access(t *testing.T) {
	detector := NewSilenceDetector()
	start := time.Now()

	var wg sync.WaitGroup
	for i := range 4 {
		wg.Add(1)
		go func(worker int) {
			defer wg.Done()
			for j := range 200 {
				at := start.Add(time.Duration(j) * time.Millisecond)
				switch worker {
				case 0:
					detector.OnAISpeechEnd(at)
				case 1:
					detector.OnUserSpeechStart(at)
				case 2:
					detector.OnUserSpeechEnd(at, j%2 == 0)
				default:
					detector.CheckSilence(at)
					detector.GetState(at)
					if j%50 == 0 {
						detector.Reset()
					}
				}
			}
		}(i)
	}
	wg.Wait()

	// A detector torn between four writers may be left in any state — what it
	// must not do is panic, lose a write, or corrupt its lock. Prove the lock
	// still works once the dust settles.
	detector.Reset()
	if should, level := detector.CheckSilence(time.Now()); should || level != 0 {
		t.Errorf("detector reports a rescue after a concurrent reset: should=%v level=%d", should, level)
	}
}
