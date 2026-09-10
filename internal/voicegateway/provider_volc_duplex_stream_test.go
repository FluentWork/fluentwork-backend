package voicegateway

import (
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
