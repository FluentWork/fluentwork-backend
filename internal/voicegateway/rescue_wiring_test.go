package voicegateway_test

import (
	"testing"

	"github.com/FluentWork/fluentwork-backend/internal/voicegateway"
)

// The gap that hid B8 for months was not a broken component — it was nobody
// calling the wiring. This test is that call.
func TestWireRescue_EnablesOnlyWhenConfigured(t *testing.T) {
	handler := voicegateway.NewHandler(nil, nil, nil, nil, voicegateway.Options{})

	if voicegateway.WireRescue(handler, voicegateway.Config{RescueEnabled: false}, nil) {
		t.Fatal("WireRescue must report false when the deployment disables it")
	}
	if handler.RescueEnabled() {
		t.Fatal("components must not be attached when rescue is disabled")
	}

	if !voicegateway.WireRescue(handler, voicegateway.Config{
		RescueEnabled:        true,
		AppServerInternalURL: "http://127.0.0.1:1",
	}, nil) {
		t.Fatal("WireRescue must report true when the deployment enables it")
	}
	if !handler.RescueEnabled() {
		t.Fatal("components must be attached when rescue is enabled")
	}
	if voicegateway.WireRescue(nil, voicegateway.Config{RescueEnabled: true}, nil) {
		t.Fatal("a nil handler is not wireable")
	}
}
