package voicegateway

import "testing"

func TestCanonicalTurnIDPrefersClient(t *testing.T) {
	t.Parallel()
	if got := canonicalTurnID(" turn-7 ", 3); got != "turn-7" {
		t.Fatalf("got %q", got)
	}
}

func TestCanonicalTurnIDFallbackSharesIOSNamespace(t *testing.T) {
	t.Parallel()
	if got := canonicalTurnID("", 3); got != "turn-3" {
		t.Fatalf("got %q, want turn-3 not volc-turn-3", got)
	}
	if got := canonicalTurnID("  ", 0); got != "" {
		t.Fatalf("seq 0 must omit, got %q", got)
	}
}
