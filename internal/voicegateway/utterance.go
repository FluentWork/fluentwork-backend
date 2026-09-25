package voicegateway

import (
	"fmt"
	"strings"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/voiceproto"
)

// AudioFormat describes PCM the client can play. Frame sizes are derived from it
// rather than declared beside it — see FrameBytes.
type AudioFormat struct {
	SampleRate    int
	Channels      int
	BitsPerSample int
}

// ClientAudioFormat is what the client plays: 16 kHz mono s16le.
//
// It is a property of the client, not a preference: LiveAudioEngine builds every
// playback buffer as 16 kHz mono int16 and memcpys the payload in, so bytes at
// another rate come out at the wrong speed and pitch.
var ClientAudioFormat = AudioFormat{SampleRate: 16000, Channels: 1, BitsPerSample: 16}

// BytesPerMS is how many bytes one millisecond of this format occupies.
//
// This number is what the rest of the package used to spell out as literals: a
// bare `32` in the pacing divisor, `3200` as a frame size, `640` as a vendor
// chunk. Each was correct and none said why, so changing the sample rate would
// have left three silently wrong constants behind.
func (f AudioFormat) BytesPerMS() int {
	if f.SampleRate <= 0 || f.Channels <= 0 || f.BitsPerSample <= 0 {
		return 0
	}
	return f.SampleRate * f.Channels * f.BitsPerSample / 8 / 1000
}

// FrameBytes is how much this format holds in the given number of milliseconds.
// Zero when the format is unusable, which callers treat as "cannot frame".
func (f AudioFormat) FrameBytes(ms int) int {
	perMS := f.BytesPerMS()
	if perMS <= 0 || ms <= 0 {
		return 0
	}
	return perMS * ms
}

// ClientFrameMS is the chunking the client's playback gate expects: whole frames
// it can drop at and below a barge-in watermark. One frame per turn would leave
// nothing to drop.
const ClientFrameMS = 100

// UtteranceKind says who is speaking. It does not change the wire format — a
// ladder rung and the AI's reply are the same frames — but it decides how
// failures are treated, and it is what keeps the ladder's own audio from being
// fed back into the state that decides when to speak it.
type UtteranceKind int

const (
	// UtteranceAI is the assistant's own reply, streamed from the provider.
	UtteranceAI UtteranceKind = iota
	// UtteranceLadder is a stuck-rescue rung, synthesized by app-server.
	UtteranceLadder
)

func (k UtteranceKind) String() string {
	switch k {
	case UtteranceAI:
		return "ai"
	case UtteranceLadder:
		return "ladder"
	default:
		return fmt.Sprintf("kind(%d)", int(k))
	}
}

// Utterance is one stretch of speech the gateway sends to the client: the AI's
// reply, or a rescue rung. Both reach the client the same way — an ai.tts.start,
// binary frames, an ai.tts.end — and until now both built those frames
// themselves, in two places, from the same wire format.
//
// One type means one encoder, one pacing rule and one end-of-stream rule. The
// Kind field is what the two producers are allowed to differ in.
type Utterance struct {
	// ID is the turn this speech belongs to. It is the same id the ladder frame
	// and the badge carry, resolved once by the turn state machine.
	ID string
	// Kind is who is speaking.
	Kind UtteranceKind
	// Format is the audio the client will play. Zero value means the client's.
	Format AudioFormat
	// FrameMS is how much audio one binary frame carries. Zero value means
	// ClientFrameMS.
	//
	// It is the same decision as the pacing interval — a frame is due after the
	// previous one has been spoken for exactly this long — so the two are derived
	// from one field rather than passed separately and left to agree.
	FrameMS int

	// VoiceID and Codec are announced in ai.tts.start; the client uses them to
	// prepare its decoder.
	VoiceID string
	Codec   string
}

// formatOrClient falls back to the client's format when the caller left it unset.
func (u Utterance) formatOrClient() AudioFormat {
	if u.Format.BytesPerMS() <= 0 {
		return ClientAudioFormat
	}
	return u.Format
}

// frameMSOrDefault is how long one frame's audio lasts.
func (u Utterance) frameMSOrDefault() int {
	if u.FrameMS > 0 {
		return u.FrameMS
	}
	return ClientFrameMS
}

// codecOrPCM names the codec announced to the client.
func (u Utterance) codecOrPCM() string {
	if c := strings.TrimSpace(u.Codec); c != "" {
		return c
	}
	return "pcm"
}

// UtteranceWriter sends one utterance. Text and audio may interleave; the caller
// decides the order, the writer owns the framing.
//
// End and Interrupt are idempotent and safe to race: whichever lands first ends
// the stream, and the other becomes a no-op rather than a second ai.tts.end for
// a stream the client has already closed.
type UtteranceWriter interface {
	// Audio sends one chunk of client-format PCM. The writer cuts it into
	// frames, numbers them, and paces them at the rate they are spoken.
	Audio(pcm []byte) error
	// End finishes the stream normally.
	End() error
	// Interrupt finishes it because the user started talking. What the client
	// is told differs (completion_status), which is why these are two methods
	// rather than one with a flag at every call site.
	Interrupt() error
}

// outboundSink is the write path a writer sends through.
//
// It is a function rather than the runtime so the writer has no opinion about
// rescue bookkeeping: whether these frames feed the silence detector is the
// caller's decision (production passes the variant that does not), and encoding
// that decision here would put rescue policy inside the audio plumbing.
type outboundSink func([]ProviderOutbound) error

// audioFramer turns PCM into numbered wire frames, holding back the partial tail
// until it is flushed.
//
// It owns the two decisions that were duplicated across the provider and the
// rescue ladder — how big a frame is, and which number it carries — and nothing
// else. In particular it does **not** write anything: the provider emits frames
// through its own error-tracking path and the rescue writer through the
// runtime's, so the framer returns frames and lets each caller send them.
type audioFramer struct {
	alloc   *SeqAllocator
	turnRef *uint32
	format  AudioFormat
	frameMS int
	pending []byte
}

func newAudioFramer(alloc *SeqAllocator, turnRef *uint32, format AudioFormat, frameMS int) *audioFramer {
	if frameMS <= 0 {
		frameMS = ClientFrameMS
	}
	return &audioFramer{alloc: alloc, turnRef: turnRef, format: format, frameMS: frameMS}
}

func (f *audioFramer) headerBytes() int {
	return voiceproto.AudioFrameLayoutFor(f.turnRef).HeaderBytes()
}

// Write buffers one chunk and returns every whole frame it completed.
func (f *audioFramer) Write(pcm []byte) [][]byte {
	f.pending = append(f.pending, pcm...)
	frameBytes := f.format.FrameBytes(f.frameMS)
	if frameBytes <= 0 {
		return nil
	}
	var frames [][]byte
	for len(f.pending) >= frameBytes {
		frames = append(frames, f.encode(f.pending[:frameBytes]))
		f.pending = f.pending[frameBytes:]
	}
	return frames
}

// Flush emits the partial tail, if any.
//
// It has to happen exactly once per stream, at the end: without it the last few
// milliseconds of speech sit in pending forever — clipped, and then prepended to
// whatever is sent next.
func (f *audioFramer) Flush() [][]byte {
	if len(f.pending) == 0 {
		return nil
	}
	frame := f.encode(f.pending)
	f.pending = nil
	return [][]byte{frame}
}

// Reset drops any buffered tail. Used when a turn is abandoned: the abandoned
// audio must not lead the next turn.
func (f *audioFramer) Reset() { f.pending = nil }

func (f *audioFramer) encode(payload []byte) []byte {
	encoded, err := (voiceproto.AITTSAudio{Seq: f.alloc.Next(), TurnRef: f.turnRef, Payload: payload}).Encode()
	if err != nil {
		// Unreachable: payload is never empty here. Returning nil would silently
		// shorten the stream, so the branch is written out rather than ignored.
		return nil
	}
	return encoded
}

// streamWriter is the only implementation of UtteranceWriter.
//
// It owns a stream: an ai.tts.start, the frames the framer produces, and exactly
// one ai.tts.end.
//
// **The AI's own audio is deliberately not sent through it**, even though it is
// the same wire format — see "Why this audio must NOT go through
// UtteranceWriter" in provider_volc_duplex.go. The short version: opening an
// ai.tts.start routes the client's frames into a decoder that production binds
// as a no-op, and the assistant goes silent on device. That was the 2026-09-12
// rollback.
//
// So the shared half is the framer below, and the unshared half is exactly the
// part that would change the wire contract.
type streamWriter struct {
	send    outboundSink
	framer  *audioFramer
	format  AudioFormat
	turnID  string
	voiceID string
	codec   string
	turnRef *uint32
	// pace is how long one frame of audio takes to speak. Injected so tests do
	// not wait in real time for a three-second rung.
	pace time.Duration
	// live reports whether this utterance still owns the client's audio stream.
	// It is checked before every frame: the user talking is the normal way a
	// ladder ends, and the frames it is still owed must not follow them into
	// their answer.
	live func() bool
	// ended guards the stream's single end frame — End and Interrupt race by
	// design, and the client must not receive two.
	ended bool
	// sentBytes is how much audio actually reached the client, for the duration
	// reported when the stream closes.
	sentBytes int
}

// beginUtterance announces a stream and returns the writer that owns it.
//
// The start frame goes out immediately and carries no audio: the client accepts
// no binary frame until it has seen one, so this cannot be deferred to the first
// Audio call.
func beginUtterance(
	u Utterance,
	send outboundSink,
	audio *SeqAllocator,
	turnRef *uint32,
	live func() bool,
) (UtteranceWriter, error) {
	if send == nil {
		return nil, fmt.Errorf("utterance: no sink")
	}
	if audio == nil {
		return nil, fmt.Errorf("utterance: no sequence allocator")
	}
	if strings.TrimSpace(u.ID) == "" {
		return nil, fmt.Errorf("utterance: id is required")
	}
	format := u.formatOrClient()
	frameMS := u.frameMSOrDefault()
	w := &streamWriter{
		send:    send,
		framer:  newAudioFramer(audio, turnRef, format, frameMS),
		format:  format,
		turnID:  u.ID,
		voiceID: u.VoiceID,
		codec:   u.codecOrPCM(),
		turnRef: turnRef,
		pace:    time.Duration(frameMS) * time.Millisecond,
		live:    live,
	}
	if err := w.send([]ProviderOutbound{{Control: voiceproto.AITTSStart{
		Type:       voiceproto.TypeAITTSStart,
		TurnID:     w.turnID,
		VoiceID:    w.voiceID,
		SampleRate: format.SampleRate,
		Codec:      w.codec,
		TurnRef:    w.turnRef,
	}}}); err != nil {
		return nil, err
	}
	return w, nil
}

// Audio sends one chunk of client-format PCM at the pace it is spoken.
func (w *streamWriter) Audio(pcm []byte) error {
	if len(pcm) == 0 {
		return nil
	}
	frameBytes := w.format.FrameBytes(w.framer.frameMS)
	if frameBytes <= 0 {
		return nil
	}
	for _, frame := range w.framer.Write(pcm) {
		if w.live != nil && !w.live() {
			// Someone else ended this utterance. The frames it is still owed
			// are owed to nobody.
			return nil
		}
		// Wait first, then send: a frame is due after the previous one has been
		// spoken for its full duration, and nothing is due at t=0 because the
		// start frame already went out.
		time.Sleep(w.pace)
		if err := w.send([]ProviderOutbound{{Binary: frame}}); err != nil {
			return err
		}
		w.sentBytes += len(frame) - w.framer.headerBytes()
	}
	return nil
}

// End finishes the stream normally.
func (w *streamWriter) End() error { return w.finish("ok") }

// Interrupt finishes it because the user started talking.
func (w *streamWriter) Interrupt() error { return w.finish("interrupted") }

func (w *streamWriter) finish(status string) error {
	if w.ended {
		return nil
	}
	w.ended = true
	// A partial tail is still owed to the client: the rung's last few
	// milliseconds are as much a part of it as the rest.
	for _, frame := range w.framer.Flush() {
		if err := w.send([]ProviderOutbound{{Binary: frame}}); err != nil {
			return err
		}
		w.sentBytes += len(frame) - w.framer.headerBytes()
	}
	// Duration is what was actually sent, not what was synthesized: a stream
	// interrupted after two frames lasted two frames, and the client uses this
	// to close out its playback accounting.
	var durationMS int
	if perMS := w.format.BytesPerMS(); perMS > 0 {
		durationMS = w.sentBytes / perMS
	}
	return w.send([]ProviderOutbound{{Control: voiceproto.AITTSEnd{
		Type:             voiceproto.TypeAITTSEnd,
		TurnID:           w.turnID,
		CompletionStatus: status,
		DurationMs:       &durationMS,
		TurnRef:          w.turnRef,
	}}})
}
