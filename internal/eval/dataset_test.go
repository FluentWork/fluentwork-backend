package eval

import (
	"encoding/json"
	"os"
	"path/filepath"
	"testing"
)

func TestWave2SynthDatasetMeetsBaseline(t *testing.T) {
	root := repoRoot(t)
	path := filepath.Join(root, "eval", "offline", "samples", "wave2-synth-v1.json")
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("read %s: %v", path, err)
	}
	var samples []Sample
	if err := json.Unmarshal(raw, &samples); err != nil {
		t.Fatalf("decode %s: %v", path, err)
	}
	if len(samples) < 50 {
		t.Fatalf("samples = %d, want >= 50", len(samples))
	}
	res := RunDataset(samples)
	if res.Failed > 0 {
		t.Fatalf("offline dataset failed %d/%d: %+v", res.Failed, res.Samples, res.Findings)
	}
}

func repoRoot(t *testing.T) string {
	t.Helper()
	dir, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	for {
		if _, err := os.Stat(filepath.Join(dir, "go.mod")); err == nil {
			return dir
		}
		parent := filepath.Dir(dir)
		if parent == dir {
			t.Fatal("go.mod not found")
		}
		dir = parent
	}
}
