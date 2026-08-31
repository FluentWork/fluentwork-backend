package main

import (
	"os"
	"testing"

	"github.com/FluentWork/fluentwork-backend/internal/voicepoc"
)

func TestRunRequireMeta12FailsWhenMissing(t *testing.T) {
	t.Setenv("VOLC_META12_QUOTA_OK", "")
	t.Setenv("VOLC_META12_NO_TRAINING_OK", "")
	t.Setenv("VOLC_META12_CONCURRENCY_OK", "")
	t.Setenv("VOLC_POC_API_KEY", "")
	t.Setenv("VOLC_SPEECH_API_KEY", "")
	t.Setenv("VOLC_SPEECH_API_KEY_DEV", "")
	t.Setenv("VOLC_POC_ENDPOINT", "")
	t.Setenv("VOLC_POC_LIVE_T9", "")

	oldArgs := os.Args
	t.Cleanup(func() { os.Args = oldArgs })
	os.Args = []string{"poc-injection-window", "--require-meta12"}

	if err := run(); err == nil {
		t.Fatal("run() expected meta12 gate error")
	}
}

func TestResolveWavPathUsesDefaultFixture(t *testing.T) {
	t.Setenv("VOLC_POC_WAV", "")

	if got := resolveWavPath(); got != voicepoc.DefaultFixtureWAVPath() {
		t.Fatalf("resolveWavPath() = %q want %q", got, voicepoc.DefaultFixtureWAVPath())
	}
}

func TestResolveWavPathUsesEnvOverride(t *testing.T) {
	t.Setenv("VOLC_POC_WAV", "tmp/custom.wav")

	if got := resolveWavPath(); got != "tmp/custom.wav" {
		t.Fatalf("resolveWavPath() = %q want %q", got, "tmp/custom.wav")
	}
}
