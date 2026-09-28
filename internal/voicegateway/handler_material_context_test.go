package voicegateway_test

import (
	"testing"

	"github.com/FluentWork/fluentwork-backend/internal/voiceproto"
)

// The whole feature end to end through the handler: app-server resolves the
// material at activation, the gateway carries its text to the provider, and the
// provider is what puts it in the system prompt. Every link is load-bearing —
// dropping any one leaves the model talking about nothing in particular.
func TestSessionStartPassesTheMaterialContextToTheProvider(t *testing.T) {
	t.Parallel()

	life := &stubLifecycle{materialContext: "The deploy is blocked on the migration."}
	providerSession := &stubProviderSession{}

	startSessionOverWSS(t, life, providerSession, voiceproto.SessionStart{
		Type:      voiceproto.TypeSessionStart,
		SceneType: "standup",
	})

	if len(providerSession.state().materials) != 1 {
		t.Fatalf("Start called %d times, want 1", len(providerSession.state().materials))
	}
	if got := providerSession.state().materials[0]; got != "The deploy is blocked on the migration." {
		t.Fatalf("provider material = %q", got)
	}
}

func TestSessionStartWithoutMaterialHandsTheProviderNothing(t *testing.T) {
	t.Parallel()

	life := &stubLifecycle{}
	providerSession := &stubProviderSession{}

	startSessionOverWSS(t, life, providerSession, voiceproto.SessionStart{
		Type: voiceproto.TypeSessionStart,
	})

	if len(providerSession.state().materials) != 1 {
		t.Fatalf("Start called %d times, want 1", len(providerSession.state().materials))
	}
	if got := providerSession.state().materials[0]; got != "" {
		t.Fatalf("provider material = %q, want empty", got)
	}
}
