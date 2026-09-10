package voicepoc

import (
	_ "embed"
	"encoding/json"
	"fmt"
	"os"
	"strings"
)

// The fixture ships inside the binary. Embedding rather than resolving a path
// removes a whole class of "which directory am I in" bug: the tests run from the
// package directory and the command runs from the repository root, so any
// relative path is correct for exactly one of them.
//
//go:embed testdata/asr_wer_samples.json
var shippedWERSamples []byte

// WERSample is one ground-truth utterance plus the traps it is built to expose.
//
// `Traps` is what makes this set diagnostic rather than a pile of sentences: a
// run can be sliced by trap and answer "which *kind* of sound is failing", which
// is the question a pipeline decision actually needs. A single aggregate WER
// cannot distinguish an accent problem from a segmentation problem.
type WERSample struct {
	ID         string   `json:"id"`
	Scene      string   `json:"scene"`
	Difficulty string   `json:"difficulty"`
	Traps      []string `json:"traps"`
	Text       string   `json:"text"`
}

// WERSampleSet is the on-disk fixture.
type WERSampleSet struct {
	Version        int               `json:"version"`
	Note           string            `json:"note"`
	TrapVocabulary map[string]string `json:"trap_vocabulary"`
	Samples        []WERSample       `json:"samples"`
}

// AudioFileName is the WAV a sample expects: `<id>.wav`, 16 kHz mono PCM.
//
// Named from the id rather than the text so a re-recording drops in without
// touching the fixture, and so a missing file is obvious rather than a silent
// mismatch.
func (s WERSample) AudioFileName() string { return s.ID + ".wav" }

// LoadWERSamples reads the fixture and validates it.
//
// Validated on load, not trusted: the fixture is data, and a duplicate id or an
// unknown trap would silently corrupt the per-trap slicing that the whole set
// exists for.
func LoadWERSamples(path string) (WERSampleSet, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return WERSampleSet{}, err
	}
	return parseWERSamples(raw, path)
}

func parseWERSamples(raw []byte, path string) (WERSampleSet, error) {
	var set WERSampleSet
	if err := json.Unmarshal(raw, &set); err != nil {
		return WERSampleSet{}, fmt.Errorf("parse %s: %w", path, err)
	}
	if len(set.Samples) == 0 {
		return WERSampleSet{}, fmt.Errorf("%s has no samples", path)
	}

	seen := make(map[string]bool, len(set.Samples))
	for i, s := range set.Samples {
		switch {
		case strings.TrimSpace(s.ID) == "":
			return WERSampleSet{}, fmt.Errorf("sample %d has no id", i)
		case seen[s.ID]:
			return WERSampleSet{}, fmt.Errorf("duplicate sample id %q", s.ID)
		case strings.TrimSpace(s.Text) == "":
			return WERSampleSet{}, fmt.Errorf("sample %q has no text", s.ID)
		}
		seen[s.ID] = true

		for _, trap := range s.Traps {
			if _, known := set.TrapVocabulary[trap]; !known {
				return WERSampleSet{}, fmt.Errorf(
					"sample %q uses trap %q, which is not in trap_vocabulary — "+
						"an unknown trap would be counted but never explained",
					s.ID, trap,
				)
			}
		}
	}
	return set, nil
}

// ShippedWERSamples parses the embedded fixture.
func ShippedWERSamples() (WERSampleSet, error) {
	return parseWERSamples(shippedWERSamples, "embedded asr_wer_samples.json")
}
