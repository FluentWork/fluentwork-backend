package voicegateway

import (
	"testing"

	"github.com/FluentWork/fluentwork-backend/internal/voicepoc"
	"github.com/FluentWork/fluentwork-backend/internal/voiceproto"
)

// ttsStarts returns the `ai.tts.start` frames in an outbound slice, in order.
func ttsStarts(outbound []ProviderOutbound) []voiceproto.AITTSStart {
	var starts []voiceproto.AITTSStart
	for _, item := range outbound {
		if start, ok := item.Control.(voiceproto.AITTSStart); ok {
			starts = append(starts, start)
		}
	}
	return starts
}

func firstBinaryIndex(outbound []ProviderOutbound) int {
	for i, item := range outbound {
		if item.Binary != nil {
			return i
		}
	}
	return -1
}

func firstStartIndex(outbound []ProviderOutbound) int {
	for i, item := range outbound {
		if _, ok := item.Control.(voiceproto.AITTSStart); ok {
			return i
		}
	}
	return -1
}

// The frame is what gives a binary frame a **turn**. Without it the client's
// dispatcher never claims the audio, and the interrupted-turn drop it exists to
// make possible has nothing to attach to — so its position matters: a start that
// arrives after the audio it introduces is worse than none, because the frames
// before it were already routed by the fallback.
func TestTTSStartPrecedesTheTurnsFirstAudioFrame(t *testing.T) {
	t.Parallel()

	sess, _ := streamableSession(t)
	sess.turnStarted = timeNow()

	outbound := sess.turnToOutbound(voicepoc.TurnResult{
		Outcome:    voicepoc.TurnOutcomeOK,
		Transcript: "go on",
		AudioPCM:   make([]byte, 48000),
	})

	starts := ttsStarts(outbound)
	if len(starts) != 1 {
		t.Fatalf("ai.tts.start count = %d, want 1", len(starts))
	}
	if s, b := firstStartIndex(outbound), firstBinaryIndex(outbound); s < 0 || b < 0 || s > b {
		t.Fatalf("start at %d, first audio at %d — the start must come first", s, b)
	}
}

// The codec must describe the bytes that actually follow. They are raw s16le PCM
// at `clientPlaybackRate`; the client's field is called `opusPayload` for
// historical reasons, and announcing "opus" here would hand PCM to an Opus
// decoder and play noise.
func TestTTSStartDescribesThePayloadItIntroduces(t *testing.T) {
	t.Parallel()

	sess, _ := streamableSession(t)
	sess.turnStarted = timeNow()

	outbound := sess.turnToOutbound(voicepoc.TurnResult{
		Outcome:  voicepoc.TurnOutcomeOK,
		AudioPCM: make([]byte, 48000),
	})

	starts := ttsStarts(outbound)
	if len(starts) != 1 {
		t.Fatalf("ai.tts.start count = %d, want 1", len(starts))
	}
	if starts[0].Codec != ttsWireCodec {
		t.Fatalf("codec = %q, want %q", starts[0].Codec, ttsWireCodec)
	}
	if starts[0].SampleRate != clientPlaybackRate {
		t.Fatalf("sample_rate = %d, want %d", starts[0].SampleRate, clientPlaybackRate)
	}
	// The client rejects anything outside this set in `prepare`, and a rejection
	// there is silence, not degradation.
	switch starts[0].SampleRate {
	case 16000, 24000, 48000:
	default:
		t.Fatalf("sample_rate %d is one the client refuses", starts[0].SampleRate)
	}
}

// A start with no frames behind it would leave the client `.active` for a turn
// that never spoke, and the turn it was withheld from is exactly the one the
// user cut off — announcing it would be announcing audio nobody will hear.
func TestWithheldAudioIsNotAnnounced(t *testing.T) {
	t.Parallel()

	sess, _ := streamableSession(t)
	sess.turnStarted = timeNow()
	sess.interruptedThisTurn = true

	outbound := sess.turnToOutbound(voicepoc.TurnResult{
		Outcome:  voicepoc.TurnOutcomeOK,
		AudioPCM: make([]byte, 48000),
	})

	if len(ttsStarts(outbound)) != 0 {
		t.Fatal("an interrupted turn announced audio it does not send")
	}
}

// The streaming path emits its frames during the turn, so the start has to be on
// that path too — the batch branch never runs for a streamed turn.
func TestTTSStartIsEmittedOnTheStreamingPath(t *testing.T) {
	t.Parallel()

	sess, emitter := streamableSession(t)

	// 48000 bytes at 24 kHz = 1 s of vendor audio, enough for whole frames.
	sess.AssistantAudio(make([]byte, 48000))

	frames := emitter.snapshotOutbound()
	if len(frames) == 0 {
		t.Fatal("no frames were emitted")
	}
	start := firstStartIndex(frames)
	binary := firstBinaryIndex(frames)
	if start < 0 {
		t.Fatal("the streaming path emitted audio with no ai.tts.start")
	}
	if start > binary {
		t.Fatalf("start at %d, first audio at %d", start, binary)
	}
}
