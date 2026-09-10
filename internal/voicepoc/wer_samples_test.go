package voicepoc

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// The shipped fixture is data, and data rots. This is the guard: it runs on the
// real file, so a typo'd trap or a duplicated id fails the build instead of
// quietly skewing the per-trap slicing the set exists for.
func TestShippedWERSamplesAreWellFormed(t *testing.T) {
	t.Parallel()

	set, err := ShippedWERSamples()
	if err != nil {
		t.Fatalf("load shipped fixture: %v", err)
	}
	// 30 is the number the plan asks for; pinning it here makes a silent
	// truncation a test failure rather than a smaller denominator.
	if len(set.Samples) != 30 {
		t.Fatalf("shipped sample count = %d, want 30", len(set.Samples))
	}

	var refWords int
	for _, s := range set.Samples {
		if len(s.Traps) == 0 {
			t.Fatalf("sample %q has no traps; it would be counted but not explained", s.ID)
		}
		refWords += len(TokenizeWER(s.Text))
	}
	if refWords < 200 {
		t.Fatalf("reference totals only %d words; a WER over that is too coarse to act on", refWords)
	}
}

// Every sample must be discoverable by the name the runner looks for, or the
// run silently reports fewer samples than it claims.
func TestWERSampleAudioFileNameFollowsTheId(t *testing.T) {
	t.Parallel()

	s := WERSample{ID: "wer-07", Text: "anything"}
	if got := s.AudioFileName(); got != "wer-07.wav" {
		t.Fatalf("AudioFileName = %q, want wer-07.wav", got)
	}
}

// Validation exists to catch the failure modes that would corrupt a run rather
// than announce themselves. Each case here is one of them.
func TestLoadWERSamplesRejectsMalformedFixtures(t *testing.T) {
	t.Parallel()

	write := func(t *testing.T, body string) string {
		t.Helper()
		path := filepath.Join(t.TempDir(), "samples.json")
		if err := os.WriteFile(path, []byte(body), 0o644); err != nil {
			t.Fatalf("write: %v", err)
		}
		return path
	}

	t.Run("duplicate id", func(t *testing.T) {
		t.Parallel()
		path := write(t, `{"trap_vocabulary":{"th":"x"},"samples":[
			{"id":"wer-01","text":"a","traps":["th"]},
			{"id":"wer-01","text":"b","traps":["th"]}]}`)
		if _, err := LoadWERSamples(path); err == nil || !strings.Contains(err.Error(), "duplicate") {
			t.Fatalf("err = %v, want a duplicate-id failure", err)
		}
	})

	t.Run("unknown trap", func(t *testing.T) {
		t.Parallel()
		// An undocumented trap is counted in the report but explained nowhere.
		path := write(t, `{"trap_vocabulary":{"th":"x"},"samples":[
			{"id":"wer-01","text":"a","traps":["th","glottal-stop"]}]}`)
		if _, err := LoadWERSamples(path); err == nil || !strings.Contains(err.Error(), "glottal-stop") {
			t.Fatalf("err = %v, want an unknown-trap failure", err)
		}
	})

	t.Run("empty text", func(t *testing.T) {
		t.Parallel()
		path := write(t, `{"trap_vocabulary":{"th":"x"},"samples":[{"id":"wer-01","text":"  ","traps":["th"]}]}`)
		if _, err := LoadWERSamples(path); err == nil || !strings.Contains(err.Error(), "no text") {
			t.Fatalf("err = %v, want a missing-text failure", err)
		}
	})

	t.Run("no samples", func(t *testing.T) {
		t.Parallel()
		path := write(t, `{"trap_vocabulary":{},"samples":[]}`)
		if _, err := LoadWERSamples(path); err == nil {
			t.Fatal("want a failure for an empty sample set")
		}
	})
}
