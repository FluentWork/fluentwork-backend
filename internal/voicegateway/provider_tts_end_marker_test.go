package voicegateway

import (
	"testing"

	"github.com/FluentWork/fluentwork-backend/internal/voicepoc"
	"github.com/FluentWork/fluentwork-backend/internal/voiceproto"
)

// A stream needs an explicit terminator.
//
// P1-16: `ai.tts.end` was emitted only by the local mock, so on the production
// path the client could not tell "the assistant finished speaking" from "the
// assistant got stuck" — both look like audio that stopped arriving. That is
// the shape that pushes an implementer to approximate the signal with a timer,
// and a timer is not the signal: it is a guess that is either early or late for
// every reply.
//
// The marker has to come *after* the audio it terminates — a terminator that
// precedes the stream is worse than none, because it reads as authoritative.
func ttsEndCount(outbound []ProviderOutbound) int {
	n := 0
	for _, item := range outbound {
		if _, ok := item.Control.(voiceproto.AITTSEnd); ok {
			n++
		}
	}
	return n
}

func TestTurnToOutboundTerminatesTheAudioStreamAfterTheAudio(t *testing.T) {
	t.Parallel()

	sess, _ := streamableSession(t)
	sess.turnStarted = timeNow()

	// 48000 bytes at 24 kHz mono s16le = 1 second of audio.
	outbound := sess.turnToOutbound(voicepoc.TurnResult{
		Outcome:  voicepoc.TurnOutcomeOK,
		AudioPCM: make([]byte, 48000),
	})

	var (
		sawEnd      bool
		lastBinary  = -1
		ttsEndIndex = -1
		marker      voiceproto.AITTSEnd
	)
	for i, item := range outbound {
		if len(item.Binary) > 0 {
			lastBinary = i
		}
		if control, ok := item.Control.(voiceproto.AITTSEnd); ok {
			sawEnd = true
			ttsEndIndex = i
			marker = control
		}
	}

	if !sawEnd {
		t.Fatalf("no ai.tts.end in the turn's outbound: the client has no terminator")
	}
	// Exactly one. A second terminator would have the client finish the same
	// stream twice, and an *early* duplicate would satisfy the ordering check
	// above while telling the client the stream ended before it started.
	if count := ttsEndCount(outbound); count != 1 {
		t.Fatalf("emitted %d ai.tts.end markers for one turn, want 1", count)
	}
	if ttsEndIndex < lastBinary {
		t.Fatalf("ai.tts.end at %d precedes the last audio frame at %d", ttsEndIndex, lastBinary)
	}
	if marker.TurnID != "turn-1" {
		t.Fatalf("turn_id = %q, want turn-1 — the marker has to name the stream it ends", marker.TurnID)
	}
	if marker.CompletionStatus != string(voicepoc.TurnOutcomeOK) {
		t.Fatalf("completion_status = %q, want %q", marker.CompletionStatus, voicepoc.TurnOutcomeOK)
	}
	if marker.DurationMs == nil || *marker.DurationMs != 1000 {
		t.Fatalf("duration_ms = %v, want 1000 for 48000 bytes at 24 kHz mono s16le", marker.DurationMs)
	}
}

// A turn that produced no audio still ends its (empty) stream. Emitting it is
// what distinguishes "finished with nothing to say" from "still going".
func TestTurnToOutboundStillTerminatesWhenThereIsNoAudio(t *testing.T) {
	t.Parallel()

	sess, _ := streamableSession(t)
	sess.turnStarted = timeNow()

	outbound := sess.turnToOutbound(voicepoc.TurnResult{Outcome: voicepoc.TurnOutcomeOK})

	for _, item := range outbound {
		if _, ok := item.Control.(voiceproto.AITTSEnd); ok {
			return
		}
	}
	t.Fatalf("a silent turn emitted no ai.tts.end: the client cannot tell it from a stalled one")
}
