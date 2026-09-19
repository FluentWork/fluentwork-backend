package voicegateway

import "testing"

// The legal path of one turn: the user talks, stops, the AI answers, the turn
// closes, and the next utterance begins a new one.
func TestTurn_HappyPath(t *testing.T) {
	turn := NewTurn("s1")

	if !turn.ApplyStart() {
		t.Fatal("user.speech.start from idle must be legal")
	}
	if got := turn.State(); got != TurnListening {
		t.Fatalf("state = %s, want listening", got)
	}
	if !turn.ApplySpeechEnd("turn-7") {
		t.Fatal("user.speech.end must be legal while listening")
	}
	if got := turn.State(); got != TurnFinalizing {
		t.Fatalf("state = %s, want finalizing", got)
	}
	if !turn.Apply(EvAIFirstOutput) {
		t.Fatal("first AI output must be legal while finalizing")
	}
	if got := turn.State(); got != TurnSpeaking {
		t.Fatalf("state = %s, want speaking", got)
	}
	if !turn.Apply(EvAIEnd) {
		t.Fatal("ai.turn.end must be legal while speaking")
	}
	if got := turn.State(); got != TurnClosed {
		t.Fatalf("state = %s, want closed", got)
	}
	// And the next utterance begins a new turn.
	if !turn.ApplyStart() {
		t.Fatal("a new utterance after a closed turn must be legal")
	}
	if n := len(turn.Rejected()); n != 0 {
		t.Fatalf("the happy path was rejected somewhere: %v", turn.Rejected())
	}
}

// A turn that ends in timeout or error still closes: the state machine tracks
// ordering, not outcomes. Which outcomes open a rescue window is rescue's
// question, asked separately.
func TestTurn_EveryAITurnEndClosesTheTurn(t *testing.T) {
	for _, setup := range []struct {
		name  string
		state func() *Turn
	}{
		{"from finalizing", func() *Turn {
			turn := NewTurn("s1")
			turn.ApplyStart()
			turn.ApplySpeechEnd("t1")
			return turn
		}},
		{"from speaking", func() *Turn {
			turn := NewTurn("s1")
			turn.ApplyStart()
			turn.ApplySpeechEnd("t1")
			turn.Apply(EvAIFirstOutput)
			return turn
		}},
	} {
		t.Run(setup.name, func(t *testing.T) {
			turn := setup.state()
			if !turn.Apply(EvAIEnd) {
				t.Fatal("ai.turn.end must close the turn")
			}
			if got := turn.State(); got != TurnClosed {
				t.Fatalf("state = %s, want closed", got)
			}
		})
	}
}

// Abandoning a recording ends the turn without an answer. The client sends
// client.turn.abort and never a user.speech.end, so the turn must not sit in
// listening forever waiting for one.
func TestTurn_AbortClosesWithoutAnAnswer(t *testing.T) {
	turn := NewTurn("s1")
	turn.ApplyStart()
	if !turn.Apply(EvTurnAbort) {
		t.Fatal("client.turn.abort must be legal while listening")
	}
	if got := turn.State(); got != TurnClosed {
		t.Fatalf("state = %s, want closed", got)
	}
}

// An abort can land after the end frame: the user hit stop while the gateway was
// already waiting for the AI. Both orders describe the same ended turn.
func TestTurn_AbortAfterSpeechEndIsStillLegal(t *testing.T) {
	turn := NewTurn("s1")
	turn.ApplyStart()
	turn.ApplySpeechEnd("t1")
	if !turn.Apply(EvTurnAbort) {
		t.Fatal("an abort arriving after speech.end must be accepted")
	}
	if got := turn.State(); got != TurnClosed {
		t.Fatalf("state = %s, want closed", got)
	}
}

// The end frame with no start before it is a real client sequence — a capture
// that began before the socket was ready never sent one — and it must not be
// treated as an ordering violation, because it is the frame that names the turn.
func TestTurn_SpeechEndWithoutStartIsAccepted(t *testing.T) {
	turn := NewTurn("s1")
	if !turn.ApplySpeechEnd("turn-3") {
		t.Fatal("user.speech.end from idle must be accepted")
	}
	if got := turn.State(); got != TurnFinalizing {
		t.Fatalf("state = %s, want finalizing", got)
	}
	if got := turn.ID(); got != "turn-3" {
		t.Fatalf("id = %q, want turn-3", got)
	}
	if n := turn.Rejected()[EvUserSpeechEnd]; n != 0 {
		t.Fatalf("rejected speech.end %d times, want 0: this sequence is expected", n)
	}
}

// Illegal orderings are refused and counted rather than absorbed. The count is
// the only way to answer "does the client ever do this" — which used to require
// a packet capture.
func TestTurn_IllegalEventsAreCountedNotAbsorbed(t *testing.T) {
	cases := []struct {
		name  string
		setup func() *Turn
		ev    TurnEvent
	}{
		{"AI output while idle", func() *Turn { return NewTurn("s1") }, EvAIFirstOutput},
		{"AI output while listening", func() *Turn {
			turn := NewTurn("s1")
			turn.ApplyStart()
			return turn
		}, EvAIFirstOutput},
		{"a second start while the AI is speaking", func() *Turn {
			turn := NewTurn("s1")
			turn.ApplyStart()
			turn.ApplySpeechEnd("t1")
			turn.Apply(EvAIFirstOutput)
			return turn
		}, EvUserSpeechStart},
	}
	for _, tc := range cases {
		t.Run(tc.name, func(t *testing.T) {
			turn := tc.setup()
			before := turn.State()
			if turn.Apply(tc.ev) {
				t.Fatalf("%s was accepted from %s", tc.ev, before)
			}
			if got := turn.State(); got != before {
				t.Fatalf("a rejected event changed the state: %s → %s", before, got)
			}
			if n := turn.Rejected()[tc.ev]; n != 1 {
				t.Fatalf("rejected count for %s = %d, want 1", tc.ev, n)
			}
		})
	}
}

// The name comes from the client when it sent one, and from the session when it
// did not — one rule, and it is the same rule for every consumer. It used to be
// three, which is how a badge and a rescue anchor could describe one utterance
// with different words.
func TestTurn_IDResolution(t *testing.T) {
	t.Run("client id wins", func(t *testing.T) {
		turn := NewTurn("s1")
		turn.ApplyStart()
		turn.ApplySpeechEnd("turn-42")
		if got := turn.ID(); got != "turn-42" {
			t.Fatalf("id = %q, want turn-42", got)
		}
	})
	t.Run("session is the fallback", func(t *testing.T) {
		turn := NewTurn("s1")
		turn.ApplyStart()
		turn.ApplySpeechEnd("")
		if got := turn.ID(); got != "s1" {
			t.Fatalf("id = %q, want s1", got)
		}
	})
	t.Run("the name survives to the end of the turn", func(t *testing.T) {
		turn := NewTurn("s1")
		turn.ApplyStart()
		turn.ApplySpeechEnd("turn-42")
		turn.Apply(EvAIFirstOutput)
		turn.Apply(EvAIEnd)
		if got := turn.ID(); got != "turn-42" {
			t.Fatalf("id after the turn closed = %q, want turn-42", got)
		}
	})
	t.Run("a turned named once keeps its name", func(t *testing.T) {
		// A rejected duplicate end must not rename the turn to the session.
		turn := NewTurn("s1")
		turn.ApplyStart()
		turn.ApplySpeechEnd("turn-42")
		turn.ApplySpeechEnd("")
		if got := turn.ID(); got != "turn-42" {
			t.Fatalf("id = %q, want the first name to stand", got)
		}
	})
}

// Nothing here panics on a nil turn: the runtime always has one, but a zero value
// appearing somewhere should degrade to "no state" rather than crash a session.
func TestTurn_NilIsInert(t *testing.T) {
	var turn *Turn
	if turn.State() != TurnIdle || turn.ID() != "" {
		t.Fatal("a nil turn must read as idle and unnamed")
	}
	if turn.ApplyStart() || turn.Apply(EvAIEnd) || turn.ApplySpeechEnd("t") {
		t.Fatal("a nil turn must reject every event")
	}
}
