package voicegateway

import "testing"

func TestCanonicalTurnIDPrefersClient(t *testing.T) {
	t.Parallel()
	if got := canonicalTurnID(" turn-7 ", "session-1"); got != "turn-7" {
		t.Fatalf("got %q, want turn-7", got)
	}
}

func TestCanonicalTurnIDFallsBackToSession(t *testing.T) {
	t.Parallel()
	if got := canonicalTurnID("", "session-1"); got != "session-1" {
		t.Fatalf("got %q, want session-1", got)
	}
	if got := canonicalTurnID("  ", "session-2"); got != "session-2" {
		t.Fatalf("got %q, want session-2", got)
	}
}
