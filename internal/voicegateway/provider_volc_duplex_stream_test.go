package voicegateway

import (
	"bytes"
	"context"
	"errors"
	"sync"
	"testing"

	"github.com/FluentWork/fluentwork-backend/internal/voiceduplex"
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

// snapshot returns everything pushed so far, in order. Ordering assertions need
// the interleaving of control and binary frames, which binaryFrames() flattens
// away.
func (r *recordingEmitter) snapshot() []ProviderOutbound {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]ProviderOutbound(nil), r.outbound...)
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

	outbound := sess.turnToOutbound(voiceduplex.TurnResult{
		AssistantText: "Sounds good.",
		Outcome:       voiceduplex.TurnOutcomeOK,
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

	outbound := sess.turnToOutbound(voiceduplex.TurnResult{
		AssistantText: "Sounds good.",
		Outcome:       voiceduplex.TurnOutcomeOK,
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
	batchOut := batchSession.turnToOutbound(voiceduplex.TurnResult{
		AssistantText: "ok", Outcome: voiceduplex.TurnOutcomeOK, AudioPCM: whole,
	})

	// Path B: chunks pushed as they arrive; the turn then closes. AudioPCM is
	// still populated, exactly as collectTurn leaves it.
	streamSession, emitter := streamableSession(t)
	for _, c := range chunks {
		streamSession.AssistantAudio(c)
	}
	streamOut := streamSession.turnToOutbound(voiceduplex.TurnResult{
		AssistantText: "ok", Outcome: voiceduplex.TurnOutcomeOK, AudioPCM: whole,
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

// A streamed turn puts the start on the wire before its first frame, and does
// not send a second one when the turn closes.
//
// This is the case that was missing. The batch path was covered (see
// assertAudioClosesBeforeTurnEnd), the streaming path was not — and that is the
// one production uses. The failure it guards against is silence on device.
func TestStreamedAudioIsPrecededByTTSStart(t *testing.T) {
	t.Parallel()

	sess, emitter := streamableSession(t)

	sess.AssistantAudio(randomPCM(t, 3200, 31))
	sess.AssistantAudio(randomPCM(t, 1600, 32))

	// The turn then closes, exactly as collectTurn leaves it: AudioPCM still
	// holds the whole turn even though every frame already went out.
	out := sess.turnToOutbound(voiceduplex.TurnResult{
		AssistantText: "ok",
		Outcome:       voiceduplex.TurnOutcomeOK,
		AudioPCM:      randomPCM(t, 4800, 33),
	})

	// What the client sees: everything pushed during the turn, then whatever the
	// closeout returns. Both halves are one wire.
	wire := append(emitter.snapshot(), out...)
	assertTTSStartBeforeAudio(t, wire)

	starts := 0
	for _, item := range wire {
		if _, ok := item.Control.(voiceproto.AITTSStart); ok {
			starts++
		}
	}
	if starts != 1 {
		t.Fatalf("ai.tts.start frames on one turn = %d, want exactly 1 — a second start closes and reopens the client's stream mid-reply", starts)
	}

	// The start names the same turn as the audio it opens; the client keys frames
	// by this id, so a mismatch attributes the audio to nobody.
	for _, item := range wire {
		start, ok := item.Control.(voiceproto.AITTSStart)
		if !ok {
			continue
		}
		if start.TurnID != "turn-1" {
			t.Fatalf("ai.tts.start turn_id = %q, want %q (the id the frames and closeout carry)", start.TurnID, "turn-1")
		}
	}
}

// An interrupted turn never opens a stream it has no audio for. The start is
// what registers a turn on the client; sending one after a barge-in would
// re-register a turn the client has already marked superseded, and any late
// frame of that turn would then be played instead of dropped.
func TestInterruptedTurnDoesNotReopenItsStream(t *testing.T) {
	t.Parallel()

	sess, emitter := streamableSession(t)
	sess.interruptedThisTurn = true

	out := sess.turnToOutbound(voiceduplex.TurnResult{
		AssistantText: "a reply the user cut off",
		Outcome:       voiceduplex.TurnOutcomeOK,
		AudioPCM:      randomPCM(t, 4800, 34),
	})

	for i, item := range append(emitter.snapshot(), out...) {
		if _, ok := item.Control.(voiceproto.AITTSStart); ok {
			t.Fatalf("ai.tts.start at [%d] on an interrupted turn — re-registers a superseded turn on the client", i)
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
		streamedBytes += len(frame) - voiceproto.AudioFrameLayoutH4.HeaderBytes()
	}

	outbound := sess.turnToOutbound(voiceduplex.TurnResult{
		AssistantText: "ok", Outcome: voiceduplex.TurnOutcomeOK, AudioPCM: pcm,
	})
	flushedBytes := 0
	for _, item := range outbound {
		if len(item.Binary) > 0 {
			flushedBytes += len(item.Binary) - voiceproto.AudioFrameLayoutH4.HeaderBytes()
		}
	}

	want := len(resampleToPlaybackRate(pcm))
	if got := streamedBytes + flushedBytes; got != want {
		t.Fatalf("client received %d audio bytes (%d streamed + %d flushed), want %d — the tail was lost",
			got, streamedBytes, flushedBytes, want)
	}
}

// 2026-09-12 真机：长 TTS 还在播，用户点开始说话。本地停播是对的，但左侧
// AI 气泡被拆成两条，后半句也不是刚问那句的回答。
//
// 当时的线上顺序是：
//
//	… mid-turn 音频 …
//	client.asr.transcription
//	ai.turn.end          ← iOS 把当前 AI item 定稿，离开 aiSpeaking
//	残留二进制音频        ← 同一句的尾巴，把会话又拉回 aiSpeaking
//	ai.tts.end
//
// iOS 在 ai.turn.end 时关气泡；其后任何音频都是新的一条。流式路径上绝大
// 部分音频已经在 collectTurn 期间发出，turnToOutbound 仍会 flush 一帧不
// 对齐的尾巴——那一帧必须在 ai.turn.end 之前。批处理路径则是整段音频都
// 走 turnToOutbound，同样不能落在定稿之后。
func TestTurnToOutbound_DoesNotFinalizeBeforeTheAudioTail(t *testing.T) {
	t.Parallel()

	t.Run("streamed leftover frame", func(t *testing.T) {
		t.Parallel()
		// 1000 vendor samples @ 24 kHz → 500 client samples @ 16 kHz.
		// audioFrameBytes is 3200 (320 samples). 500*2 = 1000 bytes of
		// payload, so the whole remainder sits in audioPending until flush.
		pcm := randomPCM(t, 1000, 5)
		sess, emitter := streamableSession(t)
		sess.AssistantAudio(pcm)

		outbound := sess.turnToOutbound(voiceduplex.TurnResult{
			Transcript:    "学习学习。",
			AssistantText: "Let's start with core terms.",
			Outcome:       voiceduplex.TurnOutcomeOK,
			AudioPCM:      pcm,
		})
		// Both halves are one wire to the client: the start that opened the
		// stream went out during the turn, the flushed tail comes back here.
		assertTTSStartBeforeAudio(t, append(emitter.snapshot(), outbound...))
		assertAudioClosesBeforeTurnEnd(t, outbound, true)
	})

	t.Run("batch audio never streamed", func(t *testing.T) {
		t.Parallel()
		pcm := randomPCM(t, 4800, 7)
		sess, _ := streamableSession(t)

		outbound := sess.turnToOutbound(voiceduplex.TurnResult{
			Transcript:    "hello",
			AssistantText: "hi there",
			Outcome:       voiceduplex.TurnOutcomeOK,
			AudioPCM:      pcm,
		})
		// Nothing streamed, so this batch carries the start as well as the audio.
		assertTTSStartBeforeAudio(t, outbound)
		assertAudioClosesBeforeTurnEnd(t, outbound, true)
	})
}

func assertAudioClosesBeforeTurnEnd(t *testing.T, outbound []ProviderOutbound, wantAudio bool) {
	t.Helper()
	turnEnd, lastAudio, ttsEnd := -1, -1, -1
	for i, item := range outbound {
		if len(item.Binary) > 0 {
			lastAudio = i
			continue
		}
		switch item.Control.(type) {
		case voiceproto.AITurnEnd:
			turnEnd = i
		case voiceproto.AITTSEnd:
			ttsEnd = i
		}
	}
	if turnEnd < 0 {
		t.Fatal("ai.turn.end missing")
	}
	if ttsEnd < 0 {
		t.Fatal("ai.tts.end missing")
	}
	if wantAudio && lastAudio < 0 {
		t.Fatal("expected audio on the closeout, got none")
	}
	if lastAudio >= 0 && lastAudio > turnEnd {
		t.Fatalf("audio at outbound[%d] follows ai.turn.end at [%d] — leftover voice after finalize splits the AI bubble", lastAudio, turnEnd)
	}
	if ttsEnd > turnEnd {
		t.Fatalf("ai.tts.end at outbound[%d] follows ai.turn.end at [%d] — terminator after finalize still reopens the turn on iOS", ttsEnd, turnEnd)
	}
	if lastAudio >= 0 && ttsEnd < lastAudio {
		t.Fatalf("ai.tts.end at outbound[%d] precedes last audio at [%d]", ttsEnd, lastAudio)
	}
}

// NOTE: this helper deliberately checks only the closeout batch it is handed.
// The ai.tts.start lives in the *streamed* half of a turn, not in the returned
// batch, so an ordering check that spans those two halves has to be made against
// the combined wire — assertTTSStartBeforeAudio, called with emitter.snapshot()
// plus the returned outbound. Passing just one half here would assert a
// property of the batch and nothing about what the client actually receives.

// assertTTSStartBeforeAudio pins the ordering the client depends on: the start
// that opens a turn's audio stream has to precede the first binary frame of that
// turn.
//
// It is not a formality. The start used to be appended in turnToOutbound, which
// on the streaming path runs long after AssistantAudio has pushed every frame;
// the client keys frames by turn, found no open stream, and dropped all of them.
// The assistant was mute on device while the transcript kept working, and no
// test on either side went red — the ordering assertions that did exist covered
// audio/ai.tts.end/ai.turn.end and never mentioned the start.
func assertTTSStartBeforeAudio(t *testing.T, wire []ProviderOutbound) {
	t.Helper()
	firstStart, firstAudio := -1, -1
	for i, item := range wire {
		if len(item.Binary) > 0 {
			if firstAudio < 0 {
				firstAudio = i
			}
			continue
		}
		if _, ok := item.Control.(voiceproto.AITTSStart); ok && firstStart < 0 {
			firstStart = i
		}
	}
	if firstAudio < 0 {
		return
	}
	if firstStart < 0 {
		t.Fatal("audio on the wire with no ai.tts.start — the client has no owner for these frames and drops them")
	}
	if firstStart > firstAudio {
		t.Fatalf("ai.tts.start at [%d] follows the first audio frame at [%d] — the stream is opened after the audio it introduces, so the client discards the reply",
			firstStart, firstAudio)
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

	outbound := sess.turnToOutbound(voiceduplex.TurnResult{
		AssistantText: "ok", Outcome: voiceduplex.TurnOutcomeOK, AudioPCM: pcm,
	})
	var sent int
	for _, item := range outbound {
		if len(item.Binary) > 0 {
			sent += len(item.Binary) - voiceproto.AudioFrameLayoutH4.HeaderBytes()
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

	outbound := sess.turnToOutbound(voiceduplex.TurnResult{
		AssistantText: "Sounds good.",
		Outcome:       voiceduplex.TurnOutcomeOK,
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
	sess.turnToOutbound(voiceduplex.TurnResult{AssistantText: "Sounds good.", Outcome: voiceduplex.TurnOutcomeOK})
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

func (r *recordingEmitter) asrTexts() []string {
	r.mu.Lock()
	defer r.mu.Unlock()
	var out []string
	for _, item := range r.outbound {
		if asr, ok := item.Control.(voiceproto.ClientASRTranscription); ok {
			out = append(out, asr.Text)
		}
	}
	return out
}

func asrControlCount(outbound []ProviderOutbound) int {
	n := 0
	for _, item := range outbound {
		if _, ok := item.Control.(voiceproto.ClientASRTranscription); ok {
			n++
		}
	}
	return n
}

func TestVolcDuplexStreamsASRAsSoonAsItArrives(t *testing.T) {
	t.Parallel()

	sess, emitter := streamableSession(t)
	sess.UserTranscript("今天学习 clean architecture。")

	got := emitter.asrTexts()
	if len(got) != 1 || got[0] != "今天学习 clean architecture。" {
		t.Fatalf("streamed ASR = %q, want the completed transcript once", got)
	}

	outbound := sess.turnToOutbound(voiceduplex.TurnResult{
		Transcript:    "今天学习 clean architecture。",
		AssistantText: "Sounds good.",
		Outcome:       voiceduplex.TurnOutcomeOK,
	})
	if asrControlCount(outbound) != 0 {
		t.Fatalf("turnToOutbound repeated client.asr.transcription after it was streamed: %+v", outbound)
	}
	if extractServerASRText(outbound) != "今天学习 clean architecture。" {
		t.Fatalf("ServerASRText missing from closeout, badge detection would break")
	}
}

func TestUserTranscriptWithoutAnEmitterDoesNotSuppressTheASR(t *testing.T) {
	t.Parallel()

	sess, _ := openMuteTestSession(t)
	sess.activeTurnID = "turn-1"
	sess.nextSeq = 1
	sess.UserTranscript("hello")
	if sess.streamedASR {
		t.Fatal("no emitter, but ASR was marked streamed — the transcript would never be sent")
	}

	outbound := sess.turnToOutbound(voiceduplex.TurnResult{
		Transcript: "hello",
		Outcome:    voiceduplex.TurnOutcomeOK,
	})
	if asrControlCount(outbound) != 1 {
		t.Fatalf("ASR control frames = %d, want the closeout frame once", asrControlCount(outbound))
	}
}

func TestUserTranscriptEmptyNeverReachesTheClient(t *testing.T) {
	t.Parallel()

	sess, emitter := streamableSession(t)
	sess.UserTranscript("")
	if len(emitter.asrTexts()) != 0 {
		t.Fatalf("empty ASR was forwarded: %q", emitter.asrTexts())
	}
	if sess.streamedASR {
		t.Fatal("empty ASR marked the turn streamed")
	}
}
