package wsframetest

import (
	"strings"
	"testing"
)

func TestDescribeSeenCollapsesRuns(t *testing.T) {
	got := describeSeen([]string{"ai.text.delta", "ai.text.delta", "ai.tts.start"})
	want := "3 frame(s): ai.text.delta×2 ai.tts.start"
	if got != want {
		t.Fatalf("got %q, want %q", got, want)
	}
}

// An empty transcript is the loudest case, not the quietest: it means the wait
// expired with nothing on the wire at all, which is a different bug from "the
// frame I wanted never came, but others did".
func TestDescribeSeenNamesTheEmptyCase(t *testing.T) {
	if got := describeSeen(nil); !strings.Contains(got, "none at all") {
		t.Fatalf("got %q, want it to say nothing arrived", got)
	}
}

func TestDescribeSeenCapsLongTranscripts(t *testing.T) {
	var seen []string
	for i := 0; i < 30; i++ {
		// Every frame a different type, so nothing collapses into a run.
		seen = append(seen, string(rune('a'+i%26))+strings.Repeat("x", i))
	}
	got := describeSeen(seen)

	if !strings.HasPrefix(got, "30 frame(s): ") {
		t.Fatalf("got %q, want it to still count every frame", got)
	}
	if !strings.Contains(got, "more runs") {
		t.Fatalf("got %q, want the middle elided", got)
	}
	// Both ends survive: the first frame says how it started, the last says
	// where it ended up.
	if !strings.Contains(got, seen[0]) || !strings.Contains(got, seen[len(seen)-1]) {
		t.Fatalf("got %q, want the first and last frames kept", got)
	}
}

func TestFrameTypeOfNamesFramelessFrames(t *testing.T) {
	cases := []struct {
		name string
		raw  map[string]any
		want string
	}{
		{"a control frame", map[string]any{"type": "ai.turn.end"}, "ai.turn.end"},
		{"no type key", map[string]any{"seq": 1.0}, "<no type>"},
		{"an empty type", map[string]any{"type": ""}, "<no type>"},
		{"a type that is not a string", map[string]any{"type": 7.0}, "<no type>"},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			if got := frameTypeOf(tc.raw); got != tc.want {
				t.Fatalf("got %q, want %q", got, tc.want)
			}
		})
	}
}
