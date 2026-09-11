package voicegateway

import (
	"testing"

	"github.com/FluentWork/fluentwork-backend/internal/voicepoc"
)

// **The user's report, at the layer that decides it.**
//
// Scenario: the assistant is answering at length, the user barge-ins, and the
// interrupted reply keeps playing — measured on 2026-09-12 as a reply the vendor
// spent 61.2 s producing and the client received as 113 frames inside 1.1 s.
//
// The gateways' reasoning used to be that none of that mattered, because "the
// client discards it at its barge-in watermark". It does not: the watermark
// records the highest sequence the client has *already seen*, and a tap that
// lands while the reply is still being produced is handled before any of its
// frames arrive — so every one of them is numbered above the watermark and every
// one is accepted. The reply then plays out in full, about a minute late.
//
// So the frames must not be sent. Frames already on the wire cannot be recalled;
// that is the client's `interruptNow()`.
func TestAnInterruptedTurnsAudioIsNotSent(t *testing.T) {
	t.Parallel()

	sess, _ := streamableSession(t)
	sess.turnStarted = timeNow()
	sess.interruptedThisTurn = true

	// 48000 bytes at 24 kHz mono s16le = 1 second of audio, so a missing
	// binary frame is not "there was nothing to send".
	outbound := sess.turnToOutbound(voicepoc.TurnResult{
		Outcome:    voicepoc.TurnOutcomeOK,
		Transcript: "go on",
		AudioPCM:   make([]byte, 48000),
	})

	for _, item := range outbound {
		if item.Binary != nil {
			t.Fatalf("an interrupted turn emitted %d audio bytes", len(item.Binary))
		}
	}
}

// The guard must not be "audio is off": an uninterrupted turn still sends it.
// Without this, the test above would pass just as well if the batch path were
// broken outright.
func TestAnUninterruptedTurnsAudioIsStillSent(t *testing.T) {
	t.Parallel()

	sess, _ := streamableSession(t)
	sess.turnStarted = timeNow()

	outbound := sess.turnToOutbound(voicepoc.TurnResult{
		Outcome:    voicepoc.TurnOutcomeOK,
		Transcript: "go on",
		AudioPCM:   make([]byte, 48000),
	})

	var bytes int
	for _, item := range outbound {
		bytes += len(item.Binary)
	}
	if bytes == 0 {
		t.Fatal("an uninterrupted turn sent no audio")
	}
}

// Withholding the send does not withhold the bill: the vendor produced this
// audio and charges for it either way. An accounting that counted only what the
// client heard would under-report by exactly the interrupted replies — which are
// the replies a user most often produces, since barge-in is how this product is
// meant to be used.
func TestWithheldAudioIsStillMetered(t *testing.T) {
	t.Parallel()

	sess, _ := streamableSession(t)
	sess.turnStarted = timeNow()
	sess.interruptedThisTurn = true

	sess.turnToOutbound(voicepoc.TurnResult{
		Outcome:  voicepoc.TurnOutcomeOK,
		AudioPCM: make([]byte, 48000),
	})

	if got := sess.usage.downlinkBytes; got == 0 {
		t.Fatal("withheld audio was not metered")
	}
}
