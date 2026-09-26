package voicegateway

import (
	"context"
	"testing"

	"github.com/FluentWork/fluentwork-backend/internal/voiceproto"
)

func TestHandler_DownlinkAudioLayoutIsLoggedWithTheFrameThatSelectedIt(t *testing.T) {
	t.Parallel()

	rig := newRescueRig(t, rescueAITurn{ttsEnd: true, turnEnd: true}, &rescueAudioSynthesizer{})
	conn, _ := rig.connect(t)

	readCtx, cancel := context.WithTimeout(context.Background(), rescueTestWait)
	defer cancel()

	rig.speakTurn(readCtx, t, conn, "t-user-1")
	rig.clock.advance(rescueTestLevel1)
	rescueWaitForType(readCtx, t, conn, voiceproto.TypeRescueLadder)

	start := rescueReadFrame(readCtx, t, conn)
	if got := start["type"]; got != voiceproto.TypeAITTSStart {
		t.Fatalf("expected %s, got %v", voiceproto.TypeAITTSStart, start)
	}
	frameTurnRef, attributed := start["turn_ref"].(float64)
	if !attributed {
		t.Fatalf("the rung's ai.tts.start carried no turn_ref, so nothing selected h8: %#v", start)
	}

	const msg = "ai.tts.start selected the downlink audio layout"

	layout, ok := rig.logs.infoAttr(msg, "layout")
	if !ok {
		t.Fatal("ai.tts.start selected a layout and nothing recorded which")
	}
	if got := layout.String(); got != "h8" {
		t.Fatalf("layout = %q, want the schema's own word for it, h8", got)
	}

	ref, ok := rig.logs.infoAttr(msg, "turn_ref")
	if !ok {
		t.Fatal("the layout record carried no turn_ref")
	}
	got, ok := ref.Any().(uint64)
	if !ok {
		t.Fatalf("turn_ref = %v (%T), want the number the frame carried", ref.Any(), ref.Any())
	}
	if float64(got) != frameTurnRef {
		t.Fatalf("recorded turn_ref = %d, want the frame's %v", got, frameTurnRef)
	}

	turnID, ok := rig.logs.infoAttr(msg, "turn_id")
	if !ok {
		t.Fatal("the layout record carried no turn_id")
	}
	if got := turnID.String(); got != "t-user-1" {
		t.Fatalf("turn_id = %q, want t-user-1", got)
	}
}
