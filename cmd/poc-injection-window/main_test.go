package main

import (
	"testing"

	"github.com/FluentWork/fluentwork-backend/internal/voicepoc"
)

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
