package voicegateway

import "sync/atomic"

// SeqAllocator owns the numbering of the binary audio frames the gateway sends
// to the client. **It belongs to the session, not to a provider.**
//
// # Why this is a type and not a field
//
// The client's barge-in gate drops every frame at or below a watermark, and that
// watermark lives for the whole WebSocket session — it is cleared by
// start/stopCapture, never between turns. So the numbering must be monotonic for
// as long as the socket is open.
//
// It used to be a `uint32` field on the provider session, which made the
// invariant depend on remembering to hand the value over whenever a session was
// replaced. It was not remembered: a transparent reopen built a fresh session
// whose counter restarted at 1, behind a watermark already in the hundreds, and
// every later frame was discarded. The transcript kept working (it does not go
// through the client's gate) and all audio was gone for the rest of the run —
// the "transcript still works, sound is dead" device report.
//
// Carrying the value across is the fix the incident got; making it impossible to
// lose is the fix the shape gets. A provider can no longer restart the numbering
// because a provider no longer holds it: it is handed an allocator at Open and
// asks it for numbers. A replaced session gets the same allocator, so there is
// nothing to carry and no caller that could forget to.
//
// # Concurrency
//
// Atomic rather than mutex-guarded, and deliberately not relying on the caller's
// lock: the sequence has two producers that do not share one — the provider's
// goroutine streaming the AI's speech, and the rescue poller speaking a ladder
// rung — and the type is the wrong place to encode which lock each holds.
type SeqAllocator struct {
	next atomic.Uint32
}

// Next returns the next frame number. Numbers start at 1: the wire format uses
// the field as an identity, so 0 is reserved for "unset" in the client's codec.
func (a *SeqAllocator) Next() uint32 {
	if a == nil {
		return 0
	}
	return a.next.Add(1)
}

// Peek reports the last number handed out, for tests and for logging.
//
// Assertions about "did the numbering continue" want this rather than a
// counter kept beside the allocator: a second counter can disagree with the one
// on the wire, which is the class of bug this type exists to remove.
func (a *SeqAllocator) Peek() uint32 {
	if a == nil {
		return 0
	}
	return a.next.Load()
}
