package voicegateway

import (
	"fmt"
	"testing"

	"github.com/FluentWork/fluentwork-backend/internal/voiceproto"
)

// A clean session must not report protocol violations, in either rescue mode.
//
// This is the test the M4 wiring shipped without, and its absence is why the
// defect survived a full unit suite for Turn: turn_test.go proves the state
// machine is correct in isolation, and nothing asked whether the gateway
// actually feeds it. It did not, in two different ways:
//
//   - The AI's end event was applied only from the rescue path, so with rescue
//     off — the production default — a finished turn sat in `finalizing` and the
//     *next* clean user.speech.start was counted as a client violation.
//   - The volc provider sends both ai.tts.end and ai.turn.end, so with rescue on
//     the end event applied twice and the second was counted.
//
// Either way the count was produced by the gateway's own frames, which is the
// one thing the counter exists to rule out (docs/99_ D1: "客户端是否重发变成一
// 个可观测的事实"). docs/101_ then told the device tester that seeing
// turn_transition_rejected every turn meant the client had changed.
func TestCleanTurnsProduceNoRejections(t *testing.T) {
	t.Parallel()

	for _, rescue := range []bool{false, true} {
		t.Run(fmt.Sprintf("rescue=%v", rescue), func(t *testing.T) {
			t.Parallel()

			rt := newTurnWiringRuntime(rescue)

			for i := 1; i <= 2; i++ {
				turnID := fmt.Sprintf("turn-%d", i)

				// What the handler does for user.speech.start / user.speech.end.
				rt.applyTurnEvent(EvUserSpeechStart)
				if !rt.turn.ApplySpeechEnd(turnID) {
					rt.reportTurnRejection(EvUserSpeechEnd)
				}

				// What sendOutbound hands to noteProviderOutbound for one clean
				// volc turn: the stream opened, spoken through, then closed with
				// both terminators, which is what the provider sends by design.
				rt.noteProviderOutbound([]ProviderOutbound{
					{Control: voiceproto.AITTSStart{
						Type: voiceproto.TypeAITTSStart, TurnID: turnID,
						VoiceID: "test-voice", SampleRate: 16000, Codec: "pcm",
					}},
					{Binary: []byte{0, 0, 0, 1, 0x11, 0x22}},
					{Binary: []byte{0, 0, 0, 2, 0x33, 0x44}},
					{Control: voiceproto.AITTSEnd{
						Type: voiceproto.TypeAITTSEnd, TurnID: turnID, CompletionStatus: "ok",
					}},
					{Control: voiceproto.AITurnEnd{
						Type: voiceproto.TypeAITurnEnd, TurnID: turnID, Outcome: "ok",
					}},
				})
			}

			if rejected := rt.turn.Rejected(); len(rejected) != 0 {
				t.Fatalf("a clean two-turn session reported protocol violations: %v — "+
					"these are the gateway's own frames, so the signal the device "+
					"checklist reads is inverted", rejected)
			}
			if got := rt.turn.State(); got != TurnClosed {
				t.Fatalf("turn state after two clean turns = %s, want %s", got, TurnClosed)
			}
			if rt.turn.ID() != "turn-2" {
				t.Fatalf("turn id = %q, want %q — the machine is not naming turns", rt.turn.ID(), "turn-2")
			}
		})
	}
}

// The AI taking the floor is what moves a turn out of `finalizing`, and it has
// to be reachable — an unreachable state is indistinguishable from a missing one.
func TestAIFirstOutputReachesSpeaking(t *testing.T) {
	t.Parallel()

	rt := newTurnWiringRuntime(false)
	rt.applyTurnEvent(EvUserSpeechStart)
	if !rt.turn.ApplySpeechEnd("turn-1") {
		t.Fatal("user.speech.end from listening must be legal")
	}
	if got := rt.turn.State(); got != TurnFinalizing {
		t.Fatalf("state before the AI speaks = %s, want %s", got, TurnFinalizing)
	}

	// Binary frames carry no Control at all, and on the streaming path they are
	// most of what the AI produces — so this is the shape that matters.
	rt.noteProviderOutbound([]ProviderOutbound{{Binary: []byte{0, 0, 0, 1, 0x11}}})

	if got := rt.turn.State(); got != TurnSpeaking {
		t.Fatalf("state after the first audio frame = %s, want %s — "+
			"the AI producing audio did not move the turn", got, TurnSpeaking)
	}
}

// An AI frame with no turn in progress is a genuine anomaly and must still be
// counted. The idempotence added for the two cases above must not swallow it.
func TestAIOutputOutsideATurnIsStillCounted(t *testing.T) {
	t.Parallel()

	rt := newTurnWiringRuntime(false)

	rt.noteProviderOutbound([]ProviderOutbound{{Binary: []byte{0, 0, 0, 1, 0x11}}})

	if got := rt.turn.Rejected()[EvAIFirstOutput]; got != 1 {
		t.Fatalf("AI output on an idle turn counted %d times, want 1 — "+
			"an anomaly the counter exists to surface", got)
	}
}

// newTurnWiringRuntime builds the minimum runtime the turn wiring touches.
//
// rescue controls only the optional half: rescueEnabled() is a nil check, so
// leaving the two collaborators nil is exactly "rescue off", which is the
// production default outside development.
func newTurnWiringRuntime(rescue bool) *sessionRuntime {
	rt := &sessionRuntime{
		sessionID: "s-wiring",
		turn:      NewTurn("s-wiring"),
	}
	if rescue {
		rt.silenceDetector = &SilenceDetector{}
		rt.rescueOrchestrator = &RescueOrchestrator{}
	}
	return rt
}
