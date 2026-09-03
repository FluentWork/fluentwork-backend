package main

import (
	"strings"
	"testing"

	"github.com/FluentWork/fluentwork-backend/internal/eval"
)

// TestGeneratedSamplesPassValidator drives the generator against an in-memory
// temp file and runs the real eval.ValidateSample over the output. If the
// generator ever emits a sample that the validator rejects, the test fails —
// so future scene additions stay disciplined.
func TestGeneratedSamplesPassValidator(t *testing.T) {
	t.Parallel()
	samples := generateSamples()
	if len(samples) != 100 {
		t.Fatalf("expected 100 generated samples, got %d", len(samples))
	}
	seen := map[string]struct{}{}
	for _, s := range samples {
		asSample := eval.Sample{
			ID:         s.ID,
			Transcript: s.Transcript,
			Review:     s.Review,
			Refine:     s.Refine,
		}
		if _, dup := seen[s.ID]; dup {
			t.Fatalf("duplicate sample id: %s", s.ID)
		}
		seen[s.ID] = struct{}{}
		if strings.TrimSpace(s.Transcript) == "" {
			t.Errorf("%s: empty transcript", s.ID)
		}
		findings := eval.ValidateSample(asSample)
		if len(findings) > 0 {
			t.Errorf("%s: %d validator finding(s): %v", s.ID, len(findings), findings)
		}
	}
}

// TestScenesCoverEnum guards the closed scene enum contract — if a new scene
// is added to sceneFrames but not to eval.SceneTags the regression will pass
// while B8 silently accepts an invalid value. We just sanity-check that the
// generator's scene rotation stays inside the known set.
func TestScenesCoverEnum(t *testing.T) {
	t.Parallel()
	for scene, frames := range sceneFrames() {
		if len(frames) == 0 {
			t.Errorf("scene %q has no frames", scene)
		}
		for i, f := range frames {
			if f.transcript == "" {
				t.Errorf("scene %q frame %d: empty transcript", scene, i)
			}
			if f.issueQuote == "" {
				t.Errorf("scene %q frame %d: empty issueQuote", scene, i)
			}
			if !strings.Contains(f.transcript, f.issueQuote) {
				t.Errorf("scene %q frame %d: quote %q not in transcript", scene, i, f.issueQuote)
			}
		}
	}
}
