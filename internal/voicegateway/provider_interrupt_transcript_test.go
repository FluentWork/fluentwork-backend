package voicegateway

import (
	"context"
	"strings"
	"testing"

	"github.com/FluentWork/fluentwork-backend/internal/voicepoc"
	"github.com/FluentWork/fluentwork-backend/internal/voiceproto"
)

func assistantUtterances(sess *volcDuplexProviderSession) []EndUtterance {
	var out []EndUtterance
	for _, u := range sess.utterances {
		if u.Speaker == "ai" {
			out = append(out, u)
		}
	}
	return out
}

// The transcript records what the user **heard**.
//
// P1-14: an interrupted reply was persisted as if fully spoken. The gateway
// streams audio as the vendor produces it and the client drops the frames that
// were in flight when the user barged in — so the user heard part of the reply,
// and the record claimed all of it. For a product whose transcript is a study
// asset, that is the record lying about what happened.
func TestInterruptedTurnRecordsOnlyWhatWasDelivered(t *testing.T) {
	t.Parallel()

	sess, _ := streamableSession(t)
	sess.turnStarted = timeNow()

	// Two deltas reach the client...
	sess.AssistantTextDelta("Sounds")
	sess.AssistantTextDelta(" good.")
	// ...then the user barges in. Everything after this point is audio the
	// client discards at its barge-in watermark.
	if _, err := sess.HandleClientControl(context.Background(), voiceproto.TypeInterrupt, nil); err != nil {
		t.Fatalf("HandleClientControl(interrupt): %v", err)
	}

	// The vendor keeps producing; the turn ends with the whole reply available.
	sess.turnToOutbound(voicepoc.TurnResult{
		AssistantText: "Sounds good. But let me add something you never heard.",
		Outcome:       voicepoc.TurnOutcomeOK,
	})

	utterances := assistantUtterances(sess)
	if len(utterances) != 1 {
		t.Fatalf("expected one assistant utterance, got %d (%+v)", len(utterances), utterances)
	}
	got := utterances[0]
	if strings.Contains(got.Text, "never heard") {
		t.Fatalf("the transcript recorded speech the user never heard: %q", got.Text)
	}
	if got.Text != "Sounds good." {
		t.Fatalf("utterance text = %q, want %q (exactly what had been delivered)", got.Text, "Sounds good.")
	}
	if !got.Interrupted {
		t.Fatalf("utterance is not marked interrupted, so a reader cannot tell it is partial")
	}
}

// An uninterrupted turn is unchanged — including the flag, because a false
// "interrupted" would make the field useless for filtering.
func TestUninterruptedTurnRecordsTheWholeReplyAndNoFlag(t *testing.T) {
	t.Parallel()

	sess, _ := streamableSession(t)
	sess.turnStarted = timeNow()

	sess.AssistantTextDelta("Sounds good.")
	sess.turnToOutbound(voicepoc.TurnResult{
		AssistantText: "Sounds good.",
		Outcome:       voicepoc.TurnOutcomeOK,
	})

	utterances := assistantUtterances(sess)
	if len(utterances) != 1 {
		t.Fatalf("expected one assistant utterance, got %d", len(utterances))
	}
	if utterances[0].Text != "Sounds good." {
		t.Fatalf("text = %q, want the whole reply", utterances[0].Text)
	}
	if utterances[0].Interrupted {
		t.Fatalf("an uninterrupted turn was marked interrupted")
	}
}

// A barge-in before any audio was delivered means the user heard nothing, so
// there is nothing to record. Emitting an empty assistant turn would put a row
// in the transcript that says the assistant spoke when it did not.
func TestInterruptBeforeAnyDeliveryRecordsNoAssistantTurn(t *testing.T) {
	t.Parallel()

	sess, _ := streamableSession(t)
	sess.turnStarted = timeNow()

	if _, err := sess.HandleClientControl(context.Background(), voiceproto.TypeInterrupt, nil); err != nil {
		t.Fatalf("HandleClientControl(interrupt): %v", err)
	}
	sess.turnToOutbound(voicepoc.TurnResult{
		AssistantText: "Never heard at all.",
		Outcome:       voicepoc.TurnOutcomeOK,
	})

	if got := assistantUtterances(sess); len(got) != 0 {
		t.Fatalf("recorded an assistant turn for speech the user never heard: %+v", got)
	}
}

// 2026-09-12 真机：长 TTS 还在播，用户点说话。iOS 先发 user.speech.start，
// 再发 interrupt。start 会 resetTurnStreamingState，把已经推给客户端的
// deliveredText 和 interruptedThisTurn 清掉，所以日志里永远是
// delivered_chars: 0。P1-14 的「转录记听到的」在这条生产帧序上成了空操作。
//
// collectingTurn 是「助手回复还没走完 turnToOutbound」：abort 漏掉的轮次
// 仍然要靠 start 清 streamed 标志（见 TestNewTurnClearsTheStreamedFlag），
// 但正在收集的这一轮不行。
func TestBargeInStartThenInterruptStillRecordsWhatWasDelivered(t *testing.T) {
	t.Parallel()

	sess, _ := streamableSession(t)
	sess.turnStarted = timeNow()
	sess.collectingTurn = true

	sess.AssistantTextDelta("Sounds")
	sess.AssistantTextDelta(" good.")

	ctx := context.Background()
	if _, err := sess.HandleClientControl(ctx, voiceproto.TypeUserSpeechStart, nil); err != nil {
		t.Fatalf("user.speech.start: %v", err)
	}
	if _, err := sess.HandleClientControl(ctx, voiceproto.TypeInterrupt, nil); err != nil {
		t.Fatalf("interrupt: %v", err)
	}

	outbound := sess.turnToOutbound(voicepoc.TurnResult{
		AssistantText: "Sounds good. But let me add something you never heard.",
		Outcome:       voicepoc.TurnOutcomeOK,
	})

	utterances := assistantUtterances(sess)
	if len(utterances) != 1 {
		t.Fatalf("expected one assistant utterance, got %d (%+v)", len(utterances), utterances)
	}
	got := utterances[0]
	if strings.Contains(got.Text, "never heard") {
		t.Fatalf("the transcript recorded speech the user never heard: %q", got.Text)
	}
	if got.Text != "Sounds good." {
		t.Fatalf("utterance text = %q, want %q (exactly what had been delivered before barge-in)", got.Text, "Sounds good.")
	}
	if !got.Interrupted {
		t.Fatalf("utterance is not marked interrupted, so a reader cannot tell it is partial")
	}

	for _, item := range outbound {
		delta, ok := item.Control.(voiceproto.AITextDelta)
		if !ok {
			continue
		}
		if strings.Contains(delta.Text, "never heard") {
			t.Fatalf("start wiped streamedText, so the unheard tail went out as a fresh delta: %q", delta.Text)
		}
	}
}

// docs/63 left "stop forwarding leftover TTS" as the other half of interrupt.
// P1-14 only truncated the transcript. After barge-in the vendor keeps
// producing; if we keep pushing those frames the client (and a late
// turnToOutbound flush) still hears speech the user already talked over.
func TestInterruptStopsForwardingFurtherAssistantAudio(t *testing.T) {
	t.Parallel()

	sess, emitter := streamableSession(t)
	sess.turnStarted = timeNow()

	sess.AssistantAudio(randomPCM(t, 4800, 11))
	before := len(emitter.binaryFrames())
	if before == 0 {
		t.Fatal("need audio on the wire before interrupt")
	}

	if _, err := sess.HandleClientControl(context.Background(), voiceproto.TypeInterrupt, nil); err != nil {
		t.Fatalf("interrupt: %v", err)
	}

	sess.AssistantAudio(randomPCM(t, 4800, 12))
	if got := len(emitter.binaryFrames()); got != before {
		t.Fatalf("forwarded %d audio frames after interrupt (had %d) — leftover TTS still reached the client", got, before)
	}
}

func TestInterruptStopsForwardingFurtherTextDeltas(t *testing.T) {
	t.Parallel()

	sess, emitter := streamableSession(t)
	sess.turnStarted = timeNow()

	sess.AssistantTextDelta("Heard this.")
	if _, err := sess.HandleClientControl(context.Background(), voiceproto.TypeInterrupt, nil); err != nil {
		t.Fatalf("interrupt: %v", err)
	}
	sess.AssistantTextDelta(" Never heard.")

	for _, fragment := range emitter.textDeltas() {
		if strings.Contains(fragment, "Never heard") {
			t.Fatalf("text after interrupt still reached the client: %q", fragment)
		}
	}

	sess.turnToOutbound(voicepoc.TurnResult{
		AssistantText: "Heard this. Never heard.",
		Outcome:       voicepoc.TurnOutcomeOK,
	})
	utterances := assistantUtterances(sess)
	if len(utterances) != 1 {
		t.Fatalf("expected one assistant utterance, got %d (%+v)", len(utterances), utterances)
	}
	if strings.Contains(utterances[0].Text, "Never heard") {
		t.Fatalf("transcript recorded speech forwarded after interrupt: %q", utterances[0].Text)
	}
	if utterances[0].Text != "Heard this." {
		t.Fatalf("utterance text = %q, want the pre-interrupt delivery", utterances[0].Text)
	}
}
