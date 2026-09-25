package voicegateway

import (
	"context"
	"encoding/binary"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/FluentWork/fluentwork-backend/internal/voiceproto"
)

// recordingSink captures what a writer puts on the wire.
type recordingSink struct {
	mu    sync.Mutex
	items []ProviderOutbound
	err   error
}

func (s *recordingSink) send(outbound []ProviderOutbound) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.err != nil {
		return s.err
	}
	s.items = append(s.items, outbound...)
	return nil
}

func (s *recordingSink) kinds() (controls []string, frames int) {
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, item := range s.items {
		switch {
		case item.Control != nil:
			switch v := item.Control.(type) {
			case voiceproto.AITTSStart:
				controls = append(controls, v.Type)
			case voiceproto.AITTSEnd:
				controls = append(controls, v.Type+":"+v.CompletionStatus)
			default:
				controls = append(controls, "other")
			}
		case len(item.Binary) > 0:
			frames++
		}
	}
	return controls, frames
}

func (s *recordingSink) seqs() []uint32 {
	s.mu.Lock()
	defer s.mu.Unlock()
	var out []uint32
	for _, item := range s.items {
		if len(item.Binary) >= 4 {
			out = append(out, binary.BigEndian.Uint32(item.Binary[:4]))
		}
	}
	return out
}

// A stream is start → frames → end, and the start carries no audio: the client
// accepts no binary frame until it has seen one, so deferring it to the first
// Audio call would drop that frame on the floor.
func TestUtterance_StartThenFramesThenEnd(t *testing.T) {
	sink := &recordingSink{}
	alloc := &SeqAllocator{}
	pcm := make([]byte, ClientAudioFormat.FrameBytes(ClientFrameMS)*3)

	w, err := beginUtterance(Utterance{
		ID: "t1", Kind: UtteranceLadder, VoiceID: "v", Codec: "pcm",
	}, sink.send, alloc, nil, nil)
	if err != nil {
		t.Fatalf("beginUtterance: %v", err)
	}
	if err := w.Audio(pcm); err != nil {
		t.Fatalf("Audio: %v", err)
	}
	if err := w.End(); err != nil {
		t.Fatalf("End: %v", err)
	}

	controls, frames := sink.kinds()
	if len(controls) != 2 || controls[0] != voiceproto.TypeAITTSStart || controls[1] != voiceproto.TypeAITTSEnd+":ok" {
		t.Fatalf("control frames = %v, want start then end:ok", controls)
	}
	if frames != 3 {
		t.Fatalf("binary frames = %d, want 3", frames)
	}
	if got := sink.seqs(); len(got) != 3 || got[0] != 1 || got[1] != 2 || got[2] != 3 {
		t.Fatalf("sequences = %v, want 1,2,3", got)
	}
}

func TestUtterance_AttributedStreamCarriesTurnRefOnStartFramesAndEnd(t *testing.T) {
	sink := &recordingSink{}
	turnRef := (&TurnRefAllocator{}).Next()
	pcm := make([]byte, ClientAudioFormat.FrameBytes(ClientFrameMS)*2)

	w, err := beginUtterance(Utterance{
		ID: "t1", Kind: UtteranceLadder, VoiceID: "v", Codec: "pcm",
	}, sink.send, &SeqAllocator{}, turnRef, nil)
	if err != nil {
		t.Fatalf("beginUtterance: %v", err)
	}
	if err := w.Audio(pcm); err != nil {
		t.Fatalf("Audio: %v", err)
	}
	if err := w.End(); err != nil {
		t.Fatalf("End: %v", err)
	}

	sink.mu.Lock()
	defer sink.mu.Unlock()

	start, ok := sink.items[0].Control.(voiceproto.AITTSStart)
	if !ok {
		t.Fatalf("first outbound is not ai.tts.start: %#v", sink.items[0])
	}
	if start.TurnRef == nil || *start.TurnRef != *turnRef {
		t.Fatalf("start turn_ref = %#v, want %d", start.TurnRef, *turnRef)
	}

	frameBytes := ClientAudioFormat.FrameBytes(ClientFrameMS)
	frames := 0
	for _, item := range sink.items {
		if len(item.Binary) == 0 {
			continue
		}
		frames++
		if len(item.Binary) != 8+frameBytes {
			t.Fatalf("frame %d is %d bytes, want an 8-byte header plus %d of payload", frames, len(item.Binary), frameBytes)
		}
		if got := binary.BigEndian.Uint32(item.Binary[4:8]); got != *turnRef {
			t.Fatalf("frame %d turn_ref = %d, want %d", frames, got, *turnRef)
		}
	}
	if frames != 2 {
		t.Fatalf("binary frames = %d, want 2", frames)
	}

	end, ok := sink.items[len(sink.items)-1].Control.(voiceproto.AITTSEnd)
	if !ok {
		t.Fatalf("last outbound is not ai.tts.end: %#v", sink.items[len(sink.items)-1])
	}
	if end.TurnRef == nil || *end.TurnRef != *turnRef {
		t.Fatalf("end turn_ref = %#v, want %d", end.TurnRef, *turnRef)
	}
}

func TestUtterance_UnattributedStreamStaysOnTheH4Layout(t *testing.T) {
	sink := &recordingSink{}
	w, err := beginUtterance(Utterance{
		ID: "t1", Kind: UtteranceLadder, VoiceID: "v", Codec: "pcm",
	}, sink.send, &SeqAllocator{}, nil, nil)
	if err != nil {
		t.Fatalf("beginUtterance: %v", err)
	}
	if err := w.Audio(make([]byte, ClientAudioFormat.FrameBytes(ClientFrameMS))); err != nil {
		t.Fatalf("Audio: %v", err)
	}
	if err := w.End(); err != nil {
		t.Fatalf("End: %v", err)
	}

	sink.mu.Lock()
	defer sink.mu.Unlock()
	start, ok := sink.items[0].Control.(voiceproto.AITTSStart)
	if !ok {
		t.Fatalf("first outbound is not ai.tts.start: %#v", sink.items[0])
	}
	if start.TurnRef != nil {
		t.Fatalf("unattributed start carries turn_ref %d", *start.TurnRef)
	}
	frameBytes := ClientAudioFormat.FrameBytes(ClientFrameMS)
	for _, item := range sink.items {
		if len(item.Binary) > 0 && len(item.Binary) != 4+frameBytes {
			t.Fatalf("unattributed frame is %d bytes, want a 4-byte header plus %d of payload", len(item.Binary), frameBytes)
		}
	}
	end, ok := sink.items[len(sink.items)-1].Control.(voiceproto.AITTSEnd)
	if !ok {
		t.Fatalf("last outbound is not ai.tts.end: %#v", sink.items[len(sink.items)-1])
	}
	if end.TurnRef != nil {
		t.Fatalf("unattributed end carries turn_ref %d", *end.TurnRef)
	}
}

// The start frame announces the format the client must prepare for, and it comes
// from the utterance's format rather than from a constant — that is what makes
// the frame size and the announced rate impossible to disagree.
func TestUtterance_StartAnnouncesTheFormatItWillSend(t *testing.T) {
	sink := &recordingSink{}
	format := AudioFormat{SampleRate: 24000, Channels: 1, BitsPerSample: 16}
	w, err := beginUtterance(Utterance{ID: "t1", Format: format}, sink.send, &SeqAllocator{}, nil, nil)
	if err != nil {
		t.Fatalf("beginUtterance: %v", err)
	}
	if err := w.Audio(make([]byte, format.FrameBytes(ClientFrameMS))); err != nil {
		t.Fatalf("Audio: %v", err)
	}

	sink.mu.Lock()
	defer sink.mu.Unlock()
	start, ok := sink.items[0].Control.(voiceproto.AITTSStart)
	if !ok {
		t.Fatalf("first control frame = %T, want AITTSStart", sink.items[0].Control)
	}
	if start.SampleRate != 24000 || start.Codec != "pcm" {
		t.Fatalf("start = %+v, want 24000/pcm", start)
	}
	// 24 kHz frames are half again as big as 16 kHz ones: the size follows the
	// announced rate instead of being a number somebody typed twice.
	if got, want := len(sink.items[1].Binary)-4, 4800; got != want {
		t.Fatalf("frame payload = %d bytes, want %d", got, want)
	}
}

// End and Interrupt race by design — the read loop interrupts while the ladder's
// goroutine is finishing — and the client must receive exactly one end.
func TestUtterance_EndAndInterruptProduceOneEndFrame(t *testing.T) {
	for _, order := range []string{"end-first", "interrupt-first"} {
		t.Run(order, func(t *testing.T) {
			sink := &recordingSink{}
			w, err := beginUtterance(Utterance{ID: "t1"}, sink.send, &SeqAllocator{}, nil, nil)
			if err != nil {
				t.Fatalf("beginUtterance: %v", err)
			}
			first, second := w.End, w.Interrupt
			if order == "interrupt-first" {
				first, second = w.Interrupt, w.End
			}
			if err := first(); err != nil {
				t.Fatalf("first: %v", err)
			}
			if err := second(); err != nil {
				t.Fatalf("second: %v", err)
			}

			controls, _ := sink.kinds()
			ends := 0
			for _, c := range controls {
				if strings.HasPrefix(c, voiceproto.TypeAITTSEnd) {
					ends++
				}
			}
			if ends != 1 {
				t.Fatalf("end frames = %d, want exactly 1 (%v)", ends, controls)
			}
		})
	}
}

// A rung the learner interrupted stops mid-stream: the frames it was still owed
// must not follow them into their answer.
func TestUtterance_StopsWhenItIsNoLongerLive(t *testing.T) {
	sink := &recordingSink{}
	live := true
	pcm := make([]byte, ClientAudioFormat.FrameBytes(ClientFrameMS)*10)

	w, err := beginUtterance(Utterance{ID: "t1"}, sink.send, &SeqAllocator{}, nil, func() bool { return live })
	if err != nil {
		t.Fatalf("beginUtterance: %v", err)
	}
	live = false
	if err := w.Audio(pcm); err != nil {
		t.Fatalf("Audio: %v", err)
	}

	_, frames := sink.kinds()
	if frames != 0 {
		t.Fatalf("frames sent after the utterance stopped being live = %d, want 0", frames)
	}
}

// Audio goes out at the pace it is spoken. Writing it all the moment it is
// synthesized buries the frames in the socket buffer, so an interrupt arrives
// after the client has already been handed the whole thing — which is how a
// ladder ends up talking over the learner who interrupted it.
func TestUtterance_PacesFramesAtTheRateTheyAreSpoken(t *testing.T) {
	sink := &recordingSink{}
	// 10 ms of audio per frame instead of 100, so the test costs 40 ms not 400.
	short := AudioFormat{SampleRate: 16000, Channels: 1, BitsPerSample: 16}
	frameBytes := short.BytesPerMS() * 10

	start := time.Now()
	w, err := beginUtterance(Utterance{ID: "t1", Format: short, FrameMS: 10}, sink.send, &SeqAllocator{}, nil, nil)
	if err != nil {
		t.Fatalf("beginUtterance: %v", err)
	}
	if err := w.Audio(make([]byte, frameBytes*4)); err != nil {
		t.Fatalf("Audio: %v", err)
	}
	elapsed := time.Since(start)

	if got := len(sink.seqs()); got != 4 {
		t.Fatalf("frames = %d, want 4", got)
	}
	// Four frames at 10 ms each: the first is due after one frame's speech, so
	// the floor is 3 intervals, not 4.
	if elapsed < 30*time.Millisecond {
		t.Fatalf("four frames went out in %s: they were not paced", elapsed)
	}
	if elapsed > 500*time.Millisecond {
		t.Fatalf("pacing took %s, far longer than the audio is worth", elapsed)
	}
}

// A stream that cannot announce itself must not send audio: the client would
// discard every frame, silently.
func TestUtterance_RefusesToStartWithoutEssentials(t *testing.T) {
	cases := []struct {
		name  string
		u     Utterance
		sink  outboundSink
		alloc *SeqAllocator
	}{
		{"no sink", Utterance{ID: "t1"}, nil, &SeqAllocator{}},
		{"no allocator", Utterance{ID: "t1"}, (&recordingSink{}).send, nil},
		{"no id", Utterance{}, (&recordingSink{}).send, &SeqAllocator{}},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if _, err := beginUtterance(tc.u, tc.sink, tc.alloc, nil, nil); err == nil {
				t.Fatal("expected an error")
			}
		})
	}
}

// The duration reported to the client is what was actually sent, not what was
// synthesized: an interrupted rung lasted as long as it lasted.
func TestUtterance_ReportsTheDurationItActuallySent(t *testing.T) {
	for _, tc := range []struct {
		name    string
		turnRef *uint32
	}{
		{"h4", nil},
		{"h8", (&TurnRefAllocator{}).Next()},
	} {
		t.Run(tc.name, func(t *testing.T) {
			sink := &recordingSink{}
			pcm := make([]byte, ClientAudioFormat.FrameBytes(1)*400) // 400 ms
			w, err := beginUtterance(Utterance{ID: "t1", FrameMS: 1}, sink.send, &SeqAllocator{}, tc.turnRef, nil)
			if err != nil {
				t.Fatalf("beginUtterance: %v", err)
			}
			if err := w.Audio(pcm); err != nil {
				t.Fatalf("Audio: %v", err)
			}
			if err := w.Interrupt(); err != nil {
				t.Fatalf("Interrupt: %v", err)
			}

			sink.mu.Lock()
			defer sink.mu.Unlock()
			end, ok := sink.items[len(sink.items)-1].Control.(voiceproto.AITTSEnd)
			if !ok {
				t.Fatalf("last control frame = %T, want AITTSEnd", sink.items[len(sink.items)-1].Control)
			}
			if end.DurationMs == nil {
				t.Fatal("duration_ms is absent")
			}
			if *end.DurationMs != 400 {
				t.Fatalf("duration = %d, want 400ms", *end.DurationMs)
			}
			if end.CompletionStatus != "interrupted" {
				t.Fatalf("status = %q, want interrupted", end.CompletionStatus)
			}
		})
	}
}

var (
	_ badgeConn = (*fakeWSConn)(nil)
	_           = websocket.MessageBinary
	_           = context.Background
)

// The rate, the frames the client receives and the chunks the vendor receives are
// one relationship, and until M3 three of them are still written as literals in
// the provider. This test states the relationship so the literals cannot drift
// apart from it in the meantime — and so M3 has an assertion to satisfy rather
// than a comment to trust.
func TestAudioSizesAreDerivedFromTheSampleRate(t *testing.T) {
	if got, want := ClientAudioFormat.BytesPerMS(), 32; got != want {
		t.Fatalf("16 kHz mono s16le = %d bytes/ms, want %d", got, want)
	}
	// What the client receives: 100 ms frames, because its playback gate drops
	// whole frames at and below a barge-in watermark and one frame per turn would
	// leave nothing to drop.
	if got, want := ClientAudioFormat.FrameBytes(ClientFrameMS), 3200; got != want {
		t.Fatalf("client frame = %d bytes, want %d (provider_volc_duplex.audioFrameBytes)", got, want)
	}
	// What the vendor receives: 20 ms chunks, which is the duplex protocol's
	// input granularity, not a client concern.
	if got, want := ClientAudioFormat.FrameBytes(20), 640; got != want {
		t.Fatalf("uplink chunk = %d bytes, want %d (voicepoc.pcm16kChunkBytes)", got, want)
	}

	// And the relationship moves with the rate: doubling it must not leave either
	// number behind. This is the assertion that fails if someone "fixes" a
	// sample rate by editing one constant.
	double := AudioFormat{SampleRate: 32000, Channels: 1, BitsPerSample: 16}
	if got, want := double.FrameBytes(ClientFrameMS), 6400; got != want {
		t.Fatalf("32 kHz client frame = %d bytes, want %d", got, want)
	}
}
