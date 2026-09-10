package voicegateway

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/FluentWork/fluentwork-backend/internal/voicepoc"
	"github.com/FluentWork/fluentwork-backend/internal/voiceproto"
)

// recordingEmitter collects everything pushed mid-turn, and can be made to fail
// so the "the client socket died under us" path is exercisable.
type recordingEmitter struct {
	mu       sync.Mutex
	outbound []ProviderOutbound
	failWith error
}

func (r *recordingEmitter) emit(item ProviderOutbound) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if r.failWith != nil {
		return r.failWith
	}
	r.outbound = append(r.outbound, item)
	return nil
}

func (r *recordingEmitter) binaryFrames() [][]byte {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out [][]byte
	for _, item := range r.outbound {
		if len(item.Binary) > 0 {
			out = append(out, item.Binary)
		}
	}
	return out
}

func (r *recordingEmitter) textDeltas() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, item := range r.outbound {
		if delta, ok := item.Control.(voiceproto.AITextDelta); ok {
			out = append(out, delta.Text)
		}
	}
	return out
}

func streamableSession(t *testing.T) (*volcDuplexProviderSession, *recordingEmitter) {
	t.Helper()
	sess, _ := openMuteTestSession(t)
	emitter := &recordingEmitter{}
	sess.SetOutboundEmitter(emitter.emit)
	sess.activeTurnID = "turn-1"
	sess.nextSeq = 1
	return sess, emitter
}

// The reply has to reach the client as it is produced. Before this, collectTurn
// accumulated the whole turn and turnToOutbound emitted it as one frame placed
// *after* ai.turn.end — which is exactly what the logs showed.
func TestVolcDuplexStreamsTextDeltasAsTheyArrive(t *testing.T) {
	t.Parallel()

	sess, emitter := streamableSession(t)

	sess.AssistantTextDelta("Sound")
	sess.AssistantTextDelta("s good")
	sess.AssistantTextDelta(".")

	got := emitter.textDeltas()
	want := []string{"Sound", "s good", "."}
	if len(got) != len(want) {
		t.Fatalf("emitted %d deltas (%q), want %d", len(got), got, len(want))
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("delta %d = %q, want %q", i, got[i], want[i])
		}
	}
}

// Once the reply has been streamed, the end-of-turn frame must not carry it
// again: the client appends ai.text.delta to the open AI item, so a repeat
// would show the whole reply twice.
//
// The *utterance* is a different matter — it is the persisted record of the
// turn, and streaming must suppress the frame without losing the record.
func TestTurnToOutbound_DoesNotRepeatAStreamedReply(t *testing.T) {
	t.Parallel()

	sess, _ := streamableSession(t)
	sess.AssistantTextDelta("Sounds good.")

	outbound := sess.turnToOutbound(voicepoc.TurnResult{
		AssistantText: "Sounds good.",
		Outcome:       voicepoc.TurnOutcomeOK,
	})

	var sawTurnEnd bool
	for _, item := range outbound {
		switch control := item.Control.(type) {
		case voiceproto.AITurnEnd:
			sawTurnEnd = true
		case voiceproto.AITextDelta:
			t.Fatalf("reply sent twice: streamed, then repeated as %q", control.Text)
		}
	}
	if !sawTurnEnd {
		t.Fatal("ai.turn.end missing — iOS needs it to finalize the AI item")
	}

	var recorded bool
	for _, u := range sess.utterances {
		if u.Speaker == "ai" && u.Text == "Sounds good." {
			recorded = true
		}
	}
	if !recorded {
		t.Fatal("the turn's utterance was not recorded; suppressing the frame must not drop the record")
	}
}

// The non-streaming path is the fallback whenever no emitter is installed, and
// it is what every provider without streaming support relies on. It must be
// untouched: the whole reply goes out in one frame at turn end.
func TestTurnToOutbound_StillSendsTheWholeReplyWithoutStreaming(t *testing.T) {
	t.Parallel()

	sess, _ := streamableSession(t)

	outbound := sess.turnToOutbound(voicepoc.TurnResult{
		AssistantText: "Sounds good.",
		Outcome:       voicepoc.TurnOutcomeOK,
	})

	var texts []string
	for _, item := range outbound {
		if delta, ok := item.Control.(voiceproto.AITextDelta); ok {
			texts = append(texts, delta.Text)
		}
	}
	if len(texts) != 1 || texts[0] != "Sounds good." {
		t.Fatalf("text frames = %q, want the whole reply once", texts)
	}
}

// The property that matters for audio, stated directly: what the client
// receives frame by frame across the turn must be exactly what the batch path
// would have sent in one go. A dropped tail is clipped speech; a repeated frame
// is a stutter; a re-resampled chunk boundary is a click.
//
// This is the audio counterpart of the text test above, and it is stronger: it
// compares the actual bytes on the wire, including the frame sequence numbers.
func TestStreamedAudioEqualsTheBatchFrames(t *testing.T) {
	t.Parallel()

	// Deliberately ragged: none of these are frame-aligned, and one is a single
	// sample, so the carry across chunk boundaries is exercised.
	chunks := [][]byte{
		randomPCM(t, 700, 11),
		randomPCM(t, 1333, 12),
		randomPCM(t, 1, 13),
		randomPCM(t, 4096, 14),
	}
	var whole []byte
	for _, c := range chunks {
		whole = append(whole, c...)
	}

	// Path A: never streamed — turnToOutbound emits the whole turn itself.
	batchSession, _ := streamableSession(t)
	batchOut := batchSession.turnToOutbound(voicepoc.TurnResult{
		AssistantText: "ok", Outcome: voicepoc.TurnOutcomeOK, AudioPCM: whole,
	})

	// Path B: chunks pushed as they arrive; the turn then closes. AudioPCM is
	// still populated, exactly as collectTurn leaves it.
	streamSession, emitter := streamableSession(t)
	for _, c := range chunks {
		streamSession.AssistantAudio(c)
	}
	streamOut := streamSession.turnToOutbound(voicepoc.TurnResult{
		AssistantText: "ok", Outcome: voicepoc.TurnOutcomeOK, AudioPCM: whole,
	})

	var batchFrames [][]byte
	for _, item := range batchOut {
		if len(item.Binary) > 0 {
			batchFrames = append(batchFrames, item.Binary)
		}
	}
	streamedFrames := emitter.binaryFrames()
	for _, item := range streamOut {
		if len(item.Binary) > 0 {
			streamedFrames = append(streamedFrames, item.Binary)
		}
	}

	if len(streamedFrames) != len(batchFrames) {
		t.Fatalf("streamed %d frames, batch would send %d — audio was dropped or duplicated",
			len(streamedFrames), len(batchFrames))
	}
	for i := range batchFrames {
		if !bytes.Equal(streamedFrames[i], batchFrames[i]) {
			t.Fatalf("frame %d differs:\n streamed %x\n batch    %x", i, streamedFrames[i], batchFrames[i])
		}
	}
}

// The trailing partial frame is the easiest thing to lose: the resampler holds
// it back because it cannot know more samples are not coming. If the turn does
// not flush it, the last few milliseconds of the assistant's speech are clipped
// — and worse, sit in the buffer to be prepended to the *next* turn's audio.
func TestTurnToOutboundFlushesTheTrailingAudio(t *testing.T) {
	t.Parallel()

	// 1000 vendor samples is 500 client samples at 3:2, which is not a whole
	// number of 320-sample frames.
	pcm := randomPCM(t, 1000, 5)
	sess, emitter := streamableSession(t)
	sess.AssistantAudio(pcm)

	streamedBytes := 0
	for _, frame := range emitter.binaryFrames() {
		streamedBytes += len(frame) - audioFrameHeaderBytes
	}

	outbound := sess.turnToOutbound(voicepoc.TurnResult{
		AssistantText: "ok", Outcome: voicepoc.TurnOutcomeOK, AudioPCM: pcm,
	})
	flushedBytes := 0
	for _, item := range outbound {
		if len(item.Binary) > 0 {
			flushedBytes += len(item.Binary) - audioFrameHeaderBytes
		}
	}

	want := len(resampleToPlaybackRate(pcm))
	if got := streamedBytes + flushedBytes; got != want {
		t.Fatalf("client received %d audio bytes (%d streamed + %d flushed), want %d — the tail was lost",
			got, streamedBytes, flushedBytes, want)
	}
}

// Same invariant as the text one: with no emitter there is nowhere to push, so
// the turn must not be marked streamed — otherwise turnToOutbound would skip
// the audio and the assistant would be silent for the whole turn.
func TestAssistantAudioWithoutAnEmitterDoesNotSuppressTheAudio(t *testing.T) {
	t.Parallel()

	sess, _ := openMuteTestSession(t) // no SetOutboundEmitter call
	pcm := randomPCM(t, 1000, 6)

	sess.AssistantAudio(pcm)
	if sess.streamedAudio {
		t.Fatal("no emitter, but the turn was marked as streamed audio — the voice would never be sent")
	}

	outbound := sess.turnToOutbound(voicepoc.TurnResult{
		AssistantText: "ok", Outcome: voicepoc.TurnOutcomeOK, AudioPCM: pcm,
	})
	var sent int
	for _, item := range outbound {
		if len(item.Binary) > 0 {
			sent += len(item.Binary) - audioFrameHeaderBytes
		}
	}
	if want := len(resampleToPlaybackRate(pcm)); sent != want {
		t.Fatalf("sent %d audio bytes, want %d", sent, want)
	}
}

// With no emitter there is nowhere to push, so the provider must not mark the
// turn as streamed. If it did, turnToOutbound would skip the end-of-turn frame
// and the client would receive no reply at all — the failure mode this whole
// change has to avoid.
func TestAssistantTextDeltaWithoutAnEmitterDoesNotSuppressTheReply(t *testing.T) {
	t.Parallel()

	sess, _ := openMuteTestSession(t) // no SetOutboundEmitter call

	sess.AssistantTextDelta("Sounds good.")

	if sess.streamedText {
		t.Fatal("no emitter, but the turn was marked streamed — the reply would never be sent")
	}

	outbound := sess.turnToOutbound(voicepoc.TurnResult{
		AssistantText: "Sounds good.",
		Outcome:       voicepoc.TurnOutcomeOK,
	})
	var texts []string
	for _, item := range outbound {
		if delta, ok := item.Control.(voiceproto.AITextDelta); ok {
			texts = append(texts, delta.Text)
		}
	}
	if len(texts) != 1 || texts[0] != "Sounds good." {
		t.Fatalf("text frames = %q, want the whole reply once", texts)
	}
}

// A push failure means the client connection is gone. It is recorded rather
// than returned — the handler discovers the dead socket on its own next write —
// but it must not be swallowed, and it must not corrupt the turn.
func TestStreamingPushFailureIsRecordedNotSwallowed(t *testing.T) {
	t.Parallel()

	sess, emitter := streamableSession(t)
	emitter.failWith = errors.New("client socket closed")

	sess.AssistantTextDelta("Sounds good.")

	if sess.emitErr == nil {
		t.Fatal("a failed push was swallowed")
	}
	// The turn still completes and still records its utterance: a dead push
	// path is the handler's problem to escalate, not a reason to lose the turn.
	sess.turnToOutbound(voicepoc.TurnResult{AssistantText: "Sounds good.", Outcome: voicepoc.TurnOutcomeOK})
	if sess.emitErr != nil {
		t.Fatal("emitErr should be cleared once it has been logged")
	}
}

// A new turn must start with a clean flag. Clearing it only at turn end would
// let an aborted turn — which never reaches turnToOutbound — suppress the *next*
// turn's reply entirely.
func TestNewTurnClearsTheStreamedFlag(t *testing.T) {
	t.Parallel()

	sess, _ := streamableSession(t)
	sess.AssistantTextDelta("Sounds good.")
	if !sess.streamedText {
		t.Fatal("streaming did not set the flag")
	}

	if _, err := sess.HandleClientControl(context.Background(), voiceproto.TypeUserSpeechStart, nil); err != nil {
		t.Fatalf("user.speech.start: %v", err)
	}
	if sess.streamedText {
		t.Fatal("a new turn began with the previous turn's streamed flag still set")
	}
}
