// Package wsframetest holds test support for reading voice-gateway control
// frames off a WebSocket.
//
// It imports no test framework on purpose. This repo's other support packages
// (configtest, metricstest) hold data rather than assertion helpers for the
// same reason: keeping `testing` out of a non-test package means a package that
// links this one does not drag the test framework into its build graph. The
// assertion — and the sentence that reports it — belong to the package whose
// test is running, so this returns an error instead of failing a test.
package wsframetest

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"time"

	"github.com/coder/websocket"
)

// AwaitType reads frames until one whose "type" is want arrives, and returns it.
//
// The error carries a transcript of the frames that arrived first, in order, and
// that transcript is the point of this function. "Read until the type I want and
// discard the rest" destroys its own evidence: by the time a wait expires, every
// frame the other side did send has been consumed and dropped by the same loop
// that is reporting the failure — so the failure reads as "context deadline
// exceeded" and says nothing about why. One real occurrence of that cost an hour
// of bisecting; with the transcript it is one line.
func AwaitType(ctx context.Context, conn *websocket.Conn, want string) (map[string]any, error) {
	start := time.Now()
	var seen []string
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return nil, fmt.Errorf("waiting for %s gave up after %s: %w\n  frames that arrived first: %s",
				want, time.Since(start).Round(time.Millisecond), err, describeSeen(seen))
		}
		var raw map[string]any
		if err := json.Unmarshal(data, &raw); err != nil {
			return nil, fmt.Errorf("decode frame while waiting for %s: %w", want, err)
		}
		if raw["type"] == want {
			return raw, nil
		}
		seen = append(seen, frameTypeOf(raw))
	}
}

// describeSeen renders the frames a wait skipped, in arrival order.
//
// Repeats collapse into runs ("ai.text.delta×3") because the useful reading is
// "what came instead, and roughly how much of it", and the list is capped for
// the same reason — a test that waited a minute can have skipped hundreds of
// frames, and the interesting ones are at both ends.
func describeSeen(seen []string) string {
	if len(seen) == 0 {
		return "(none at all — nothing was written to this socket)"
	}

	type run struct {
		typ  string
		reps int
	}
	var runs []run
	for _, typ := range seen {
		if n := len(runs); n > 0 && runs[n-1].typ == typ {
			runs[n-1].reps++
			continue
		}
		runs = append(runs, run{typ: typ, reps: 1})
	}

	render := func(r run) string {
		if r.reps == 1 {
			return r.typ
		}
		return fmt.Sprintf("%s×%d", r.typ, r.reps)
	}

	const head, tail = 6, 3
	var parts []string
	switch {
	case len(runs) <= head+tail:
		for _, r := range runs {
			parts = append(parts, render(r))
		}
	default:
		for _, r := range runs[:head] {
			parts = append(parts, render(r))
		}
		parts = append(parts, fmt.Sprintf("… (%d more runs)", len(runs)-head-tail))
		for _, r := range runs[len(runs)-tail:] {
			parts = append(parts, render(r))
		}
	}
	return fmt.Sprintf("%d frame(s): %s", len(seen), strings.Join(parts, " "))
}

// frameTypeOf names a decoded frame for a transcript, including the ones that
// are not shaped like control frames at all.
func frameTypeOf(raw map[string]any) string {
	if typ, ok := raw["type"].(string); ok && typ != "" {
		return typ
	}
	return "<no type>"
}
