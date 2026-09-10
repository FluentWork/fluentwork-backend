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
	fragments []string
	audio     [][]byte
}

func (r *recordingSink) AssistantTextDelta(delta string) {
	r.fragments = append(r.fragments, delta)
}

func (r *recordingSink) AssistantAudio(pcm []byte) {
	// Copy: the caller reuses nothing today, but a sink that kept a reference
	// into a reused buffer would be a heisenbug to chase later.
	r.audio = append(r.audio, append([]byte(nil), pcm...))
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
