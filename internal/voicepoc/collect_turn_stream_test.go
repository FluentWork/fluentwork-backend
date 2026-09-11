package voicepoc

import (
	"bytes"
	"context"
	"encoding/base64"
	"strings"
	"testing"
	"time"

	"github.com/coder/websocket"
)

// recordingSink captures the fragments one collectTurn forwards.
//
// No mutex: collectTurn calls the sink on its own goroutine, which is the
// goroutine running the test.
type recordingSink struct {
	fragments   []string
	audio       [][]byte
	transcripts []string
}

func (r *recordingSink) AssistantTextDelta(delta string) {
	r.fragments = append(r.fragments, delta)
}

func (r *recordingSink) AssistantAudio(pcm []byte) {
	// Copy: the caller reuses nothing today, but a sink that kept a reference
	// into a reused buffer would be a heisenbug to chase later.
	r.audio = append(r.audio, append([]byte(nil), pcm...))
}

func (r *recordingSink) UserTranscript(text string) {
	r.transcripts = append(r.transcripts, text)
}

// collectTurn is a blocking accumulator: text streams into it from the vendor
// but the caller only saw the result once the turn closed. The sink is what
// lets the gateway forward each fragment as it is produced — the acceptance
// criterion for P1-2 is that ai.text.delta becomes a real delta rather than the
// whole reply in one frame.
func TestCollectTurn_ForwardsTextFragmentsToTheSink(t *testing.T) {
	t.Parallel()

	url := startDuplexMockServer(t, func(conn *websocket.Conn) {
		readUntilType(t, conn, "session.create")
		writeJSONFrame(t, conn, `{"type":"session.created","session":{"id":"sess-stream"}}`)
		writeJSONFrame(t, conn, `{"type":"conversation.item.input_audio_transcription.started"}`)
		writeJSONFrame(t, conn, `{"type":"conversation.item.input_audio_transcription.completed","transcript":"hello"}`)
		writeJSONFrame(t, conn, `{"type":"response.output_text.delta","delta":"Sound"}`)
		writeJSONFrame(t, conn, `{"type":"response.output_text.delta","delta":"s good"}`)
		writeJSONFrame(t, conn, `{"type":"response.output_text.delta","delta":"."}`)
		writeJSONFrame(t, conn, `{"type":"response.output_text.done","text":"Sounds good."}`)
		writeJSONFrame(t, conn, `{"type":"response.done"}`)
		<-make(chan struct{})
	})

	session := openTestSession(t, url)
	defer func() { _ = session.Close(context.Background()) }()
	sink := &recordingSink{}
	session.SetTurnSink(sink)

	started := time.Now()
	turn, err := session.collectTurn(context.Background(), started, nil, 5*time.Second)
	if err != nil {
		t.Fatalf("collectTurn: %v", err)
	}

	// Three separate fragments in arrival order — not one frame holding the
	// whole reply. This is the whole point of the sink.
	want := []string{"Sound", "s good", "."}
	if len(sink.fragments) != len(want) {
		t.Fatalf("sink received %d fragments (%q), want %d", len(sink.fragments), sink.fragments, len(want))
	}
	for i, fragment := range want {
		if sink.fragments[i] != fragment {
			t.Fatalf("fragment %d = %q, want %q", i, sink.fragments[i], fragment)
		}
	}

	// The turn result is unchanged. AssistantText is still the whole reply:
	// it is what gets recorded as the turn's utterance and persisted, so
	// streaming must not turn it into a fragment.
	if turn.AssistantText != "Sounds good." {
		t.Fatalf("AssistantText = %q, want the whole reply", turn.AssistantText)
	}
}

// Audio is the half of the turn a user actually hears. Streaming it is what
// makes the assistant *speak* sooner instead of after the whole turn has been
// generated.
func TestCollectTurn_ForwardsAudioChunksToTheSink(t *testing.T) {
	t.Parallel()

	chunks := [][]byte{{1, 2, 3, 4}, {5, 6, 7, 8}, {9, 10}}
	url := startDuplexMockServer(t, func(conn *websocket.Conn) {
		readUntilType(t, conn, "session.create")
		writeJSONFrame(t, conn, `{"type":"session.created","session":{"id":"sess-audio"}}`)
		writeJSONFrame(t, conn, `{"type":"conversation.item.input_audio_transcription.completed","transcript":"hello"}`)
		for _, chunk := range chunks {
			writeJSONFrame(t, conn, `{"type":"response.output_audio.delta","delta":"`+
				base64.StdEncoding.EncodeToString(chunk)+`"}`)
		}
		writeJSONFrame(t, conn, `{"type":"response.done"}`)
		<-make(chan struct{})
	})

	session := openTestSession(t, url)
	defer func() { _ = session.Close(context.Background()) }()

	sink := &recordingSink{}
	session.SetTurnSink(sink)

	started := time.Now()
	turn, err := session.collectTurn(context.Background(), started, nil, 5*time.Second)
	if err != nil {
		t.Fatalf("collectTurn: %v", err)
	}

	// Each vendor chunk delivered on its own, decoded, in order.
	if len(sink.audio) != len(chunks) {
		t.Fatalf("sink received %d audio chunks, want %d", len(sink.audio), len(chunks))
	}
	for i, want := range chunks {
		if !bytes.Equal(sink.audio[i], want) {
			t.Fatalf("audio chunk %d = %v, want %v", i, sink.audio[i], want)
		}
	}

	// And the turn still carries the whole thing: it is what the usage
	// accounting is measured from and what the non-streaming path emits.
	var want []byte
	for _, chunk := range chunks {
		want = append(want, chunk...)
	}
	if !bytes.Equal(turn.AudioPCM, want) {
		t.Fatalf("AudioPCM = %v, want the concatenation %v", turn.AudioPCM, want)
	}
}

// Empty audio chunks must not reach the sink, for the same reason empty text
// fragments must not: "the sink was called" has to mean something was sent.
func TestCollectTurn_EmptyAudioDeltaNeverReachesTheSink(t *testing.T) {
	t.Parallel()

	url := startDuplexMockServer(t, func(conn *websocket.Conn) {
		readUntilType(t, conn, "session.create")
		writeJSONFrame(t, conn, `{"type":"session.created","session":{"id":"sess-empty-audio"}}`)
		writeJSONFrame(t, conn, `{"type":"conversation.item.input_audio_transcription.completed","transcript":"hello"}`)
		writeJSONFrame(t, conn, `{"type":"response.output_audio.delta","delta":""}`)
		writeJSONFrame(t, conn, `{"type":"response.done"}`)
		<-make(chan struct{})
	})

	session := openTestSession(t, url)
	defer func() { _ = session.Close(context.Background()) }()

	sink := &recordingSink{}
	session.SetTurnSink(sink)

	started := time.Now()
	if _, err := session.collectTurn(context.Background(), started, nil, 5*time.Second); err != nil {
		t.Fatalf("collectTurn: %v", err)
	}
	if len(sink.audio) != 0 {
		t.Fatalf("sink received %d audio chunks, want none for an empty delta", len(sink.audio))
	}
}

// response.output_text.done carries the authoritative full text, and the vendor
// is not obliged to make it equal the concatenated deltas. When it differs the
// client has already been shown the deltas while the server is about to record
// `text` — review would disagree with what the user just read on screen.
//
// Not corrected (the protocol has no frame that replaces streamed text), only
// logged. Pinned here so that is a decision on record rather than an accident.
func TestCollectTurn_DoneTextWinsOverStreamedDeltas(t *testing.T) {
	t.Parallel()

	url := startDuplexMockServer(t, func(conn *websocket.Conn) {
		readUntilType(t, conn, "session.create")
		writeJSONFrame(t, conn, `{"type":"session.created","session":{"id":"sess-revised"}}`)
		writeJSONFrame(t, conn, `{"type":"conversation.item.input_audio_transcription.started"}`)
		writeJSONFrame(t, conn, `{"type":"conversation.item.input_audio_transcription.completed","transcript":"hello"}`)
		writeJSONFrame(t, conn, `{"type":"response.output_text.delta","delta":"Sound"}`)
		writeJSONFrame(t, conn, `{"type":"response.output_text.delta","delta":"s good"}`)
		// Revised after streaming: no trailing period, and a word changed.
		writeJSONFrame(t, conn, `{"type":"response.output_text.done","text":"Sounds great"}`)
		writeJSONFrame(t, conn, `{"type":"response.done"}`)
		<-make(chan struct{})
	})

	session := openTestSession(t, url)
	defer func() { _ = session.Close(context.Background()) }()

	sink := &recordingSink{}
	session.SetTurnSink(sink)

	started := time.Now()
	turn, err := session.collectTurn(context.Background(), started, nil, 5*time.Second)
	if err != nil {
		t.Fatalf("collectTurn: %v", err)
	}

	if got := strings.Join(sink.fragments, ""); got != "Sounds good" {
		t.Fatalf("client saw %q, want the streamed deltas", got)
	}
	if turn.AssistantText != "Sounds great" {
		t.Fatalf("AssistantText = %q, want the authoritative done text", turn.AssistantText)
	}
}

// A turn that produces no text at all must not call the sink. The gateway
// relies on "no fragments" meaning "nothing was streamed", so an empty
// delivery would set that flag without putting anything on the wire.
func TestCollectTurn_EmptyDeltaNeverReachesTheSink(t *testing.T) {
	t.Parallel()

	url := startDuplexMockServer(t, func(conn *websocket.Conn) {
		readUntilType(t, conn, "session.create")
		writeJSONFrame(t, conn, `{"type":"session.created","session":{"id":"sess-empty"}}`)
		writeJSONFrame(t, conn, `{"type":"conversation.item.input_audio_transcription.started"}`)
		writeJSONFrame(t, conn, `{"type":"conversation.item.input_audio_transcription.completed","transcript":"hello"}`)
		writeJSONFrame(t, conn, `{"type":"response.output_text.delta","delta":""}`)
		writeJSONFrame(t, conn, `{"type":"response.done"}`)
		<-make(chan struct{})
	})

	session := openTestSession(t, url)
	defer func() { _ = session.Close(context.Background()) }()

	sink := &recordingSink{}
	session.SetTurnSink(sink)

	started := time.Now()
	if _, err := session.collectTurn(context.Background(), started, nil, 5*time.Second); err != nil {
		t.Fatalf("collectTurn: %v", err)
	}

	if len(sink.fragments) != 0 {
		t.Fatalf("sink received %q, want nothing for an empty delta", sink.fragments)
	}
}

// 2026-09-12: turn-2 and turn-3 event_types both started with response.done.
// collectTurn already ignores a leftover done until this turn has
// user+response progress. It did not ignore leftover output_text.delta /
// output_audio.delta, so a late chunk from the previous generation would be
// spliced into the new reply — the client hears the old lecture continue.
func TestCollectTurn_DropsStaleResponseDeltasUntilThisTurnHasUserProgress(t *testing.T) {
	t.Parallel()

	staleAudio := []byte{9, 9, 9, 9}
	freshAudio := []byte{1, 2, 3, 4}
	url := startDuplexMockServer(t, func(conn *websocket.Conn) {
		readUntilType(t, conn, "session.create")
		writeJSONFrame(t, conn, `{"type":"session.created","session":{"id":"sess-stale"}}`)
		// Leftover from the previous turn, already sitting on the socket.
		writeJSONFrame(t, conn, `{"type":"response.output_text.delta","delta":"Want to break down its key parts?"}`)
		writeJSONFrame(t, conn, `{"type":"response.output_audio.delta","delta":"`+
			base64.StdEncoding.EncodeToString(staleAudio)+`"}`)
		writeJSONFrame(t, conn, `{"type":"response.output_text.done","text":"Want to break down its key parts?"}`)
		writeJSONFrame(t, conn, `{"type":"response.done"}`)
		// This turn.
		writeJSONFrame(t, conn, `{"type":"conversation.item.input_audio_transcription.completed","transcript":"学习学习。"}`)
		writeJSONFrame(t, conn, `{"type":"response.output_text.delta","delta":"Let's start with core terms."}`)
		writeJSONFrame(t, conn, `{"type":"response.output_audio.delta","delta":"`+
			base64.StdEncoding.EncodeToString(freshAudio)+`"}`)
		writeJSONFrame(t, conn, `{"type":"response.done"}`)
		<-make(chan struct{})
	})

	session := openTestSession(t, url)
	defer func() { _ = session.Close(context.Background()) }()
	sink := &recordingSink{}
	session.SetTurnSink(sink)

	turn, err := session.collectTurn(context.Background(), time.Now(), nil, 5*time.Second)
	if err != nil {
		t.Fatalf("collectTurn: %v", err)
	}

	if turn.Transcript != "学习学习。" {
		t.Fatalf("Transcript = %q, want this turn's ASR", turn.Transcript)
	}
	if turn.AssistantText != "Let's start with core terms." {
		t.Fatalf("AssistantText = %q, want this turn only — leftover previous-turn text was spliced in", turn.AssistantText)
	}
	if got := strings.Join(sink.fragments, ""); got != "Let's start with core terms." {
		t.Fatalf("client saw %q, want this turn's deltas only", got)
	}
	if len(sink.audio) != 1 || !bytes.Equal(sink.audio[0], freshAudio) {
		t.Fatalf("client audio = %v, want only this turn's chunk %v", sink.audio, freshAudio)
	}
	if !bytes.Equal(turn.AudioPCM, freshAudio) {
		t.Fatalf("AudioPCM = %v, want this turn only", turn.AudioPCM)
	}
}

// 2026-09-12 session d5090fe1: collectTurn treated output_audio.done as
// terminal. Volc still sends response.done after that. The leftover done
// became turn-2's first event; ASR started and the buffer committed, then
// Volc produced nothing for 60s (outcome=partial, empty transcript).
//
// response.done is the vendor's turn terminator. audio.done only means the
// audio stream finished.
func TestCollectTurn_DoesNotFinishOnOutputAudioDoneBeforeResponseDone(t *testing.T) {
	t.Parallel()

	audio := []byte{1, 2, 3, 4}
	firstDone := make(chan struct{})
	url := startDuplexMockServer(t, func(conn *websocket.Conn) {
		readUntilType(t, conn, "session.create")
		writeJSONFrame(t, conn, `{"type":"session.created","session":{"id":"sess-audio-done"}}`)
		writeJSONFrame(t, conn, `{"type":"conversation.item.input_audio_transcription.completed","transcript":"hello"}`)
		writeJSONFrame(t, conn, `{"type":"response.output_text.delta","delta":"Hi."}`)
		writeJSONFrame(t, conn, `{"type":"response.output_audio.delta","delta":"`+
			base64.StdEncoding.EncodeToString(audio)+`"}`)
		writeJSONFrame(t, conn, `{"type":"response.output_audio.done"}`)
		time.Sleep(80 * time.Millisecond)
		writeJSONFrame(t, conn, `{"type":"response.done"}`)
		<-firstDone
		writeJSONFrame(t, conn, `{"type":"conversation.item.input_audio_transcription.completed","transcript":"next"}`)
		writeJSONFrame(t, conn, `{"type":"response.output_text.delta","delta":"Okay."}`)
		writeJSONFrame(t, conn, `{"type":"response.done"}`)
		<-make(chan struct{})
	})

	session := openTestSession(t, url)
	defer func() { _ = session.Close(context.Background()) }()

	turn1, err := session.collectTurn(context.Background(), time.Now(), nil, 5*time.Second)
	if err != nil {
		t.Fatalf("collectTurn turn-1: %v", err)
	}
	if turn1.Outcome != TurnOutcomeOK {
		t.Fatalf("turn-1 outcome = %q, want ok", turn1.Outcome)
	}
	if !eventTypesContain(turn1.EventTypes, "response.done") {
		t.Fatalf("turn-1 event_types = %v, want response.done consumed in this turn (not leaked)", turn1.EventTypes)
	}
	last := turn1.EventTypes[len(turn1.EventTypes)-1]
	if last != "response.done" {
		t.Fatalf("turn-1 ended on %q, want response.done", last)
	}

	close(firstDone)
	turn2, err := session.collectTurn(context.Background(), time.Now(), nil, 5*time.Second)
	if err != nil {
		t.Fatalf("collectTurn turn-2: %v", err)
	}
	if len(turn2.EventTypes) == 0 {
		t.Fatal("turn-2 saw no events")
	}
	if turn2.EventTypes[0] == "response.done" {
		t.Fatalf("turn-1 leaked response.done into turn-2: %v", turn2.EventTypes)
	}
	if turn2.Transcript != "next" || turn2.AssistantText != "Okay." {
		t.Fatalf("turn-2 transcript=%q text=%q, want this turn only", turn2.Transcript, turn2.AssistantText)
	}
}

func eventTypesContain(types []string, want string) bool {
	for _, typ := range types {
		if typ == want {
			return true
		}
	}
	return false
}

// The user bubble shows "正在转写…" until client.asr.transcription arrives.
// collectTurn used to keep ASR until response.done, so the bubble stayed
// transcribing until the assistant's TTS was already on the wire — independent
// of how long the user spoke (ASR is ready seconds earlier).
func TestCollectTurn_ForwardsUserTranscriptWhenASRCompletes(t *testing.T) {
	t.Parallel()

	releaseReply := make(chan struct{})
	url := startDuplexMockServer(t, func(conn *websocket.Conn) {
		readUntilType(t, conn, "session.create")
		writeJSONFrame(t, conn, `{"type":"session.created","session":{"id":"sess-asr-stream"}}`)
		writeJSONFrame(t, conn, `{"type":"conversation.item.input_audio_transcription.completed","transcript":"今天学习 clean architecture。"}`)
		<-releaseReply
		writeJSONFrame(t, conn, `{"type":"response.output_text.delta","delta":"Sounds good."}`)
		writeJSONFrame(t, conn, `{"type":"response.done"}`)
		<-make(chan struct{})
	})

	session := openTestSession(t, url)
	defer func() { _ = session.Close(context.Background()) }()

	gotASR := make(chan string, 1)
	session.SetTurnSink(&asrWaitSink{got: gotASR})

	errCh := make(chan error, 1)
	go func() {
		_, err := session.collectTurn(context.Background(), time.Now(), nil, 5*time.Second)
		errCh <- err
	}()

	select {
	case text := <-gotASR:
		if text != "今天学习 clean architecture。" {
			t.Fatalf("forwarded %q, want the completed ASR", text)
		}
	case <-time.After(2 * time.Second):
		close(releaseReply)
		t.Fatal("ASR was not forwarded until the assistant reply closed the turn")
	}
	close(releaseReply)
	if err := <-errCh; err != nil {
		t.Fatalf("collectTurn: %v", err)
	}
}

type asrWaitSink struct {
	got chan string
}

func (s *asrWaitSink) AssistantTextDelta(string) {}
func (s *asrWaitSink) AssistantAudio([]byte)     {}
func (s *asrWaitSink) UserTranscript(text string) {
	select {
	case s.got <- text:
	default:
	}
}
