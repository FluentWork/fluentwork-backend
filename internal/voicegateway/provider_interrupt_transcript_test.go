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
