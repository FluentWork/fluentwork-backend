package voicegateway

import (
	"strings"
	"sync"

	"github.com/FluentWork/fluentwork-backend/internal/voiceproto"
)

// TurnState is where one conversational turn stands.
//
// The gateway used to have no such word: a turn's state was spread over
// `collecting` on the runtime, `activeTurnID` and `turnStarted` on the provider
// session, and `rescueTurnID` beside them. Nothing could answer "what is this
// turn doing right now", which is why every fix landed as another field
// somewhere (docs/94_ F1).
type TurnState int

const (
	// TurnIdle means the floor is nobody's — the AI has finished and the user has not
	// started.
	TurnIdle TurnState = iota
	// TurnListening means the user is speaking.
	TurnListening
	// TurnFinalizing means the user stopped and the gateway is waiting for the AI.
	TurnFinalizing
	// TurnSpeaking means the AI has begun producing output for this turn.
	TurnSpeaking
	// TurnClosed means the turn is over, normally or not.
	TurnClosed
)

func (s TurnState) String() string {
	switch s {
	case TurnIdle:
		return "idle"
	case TurnListening:
		return "listening"
	case TurnFinalizing:
		return "finalizing"
	case TurnSpeaking:
		return "speaking"
	case TurnClosed:
		return "closed"
	default:
		return "unknown"
	}
}

// TurnEvent is something that happened, named after the wire frame that carried
// it.
type TurnEvent int

const (
	// EvUserSpeechStart is the user beginning to speak.
	EvUserSpeechStart TurnEvent = iota
	// EvUserSpeechEnd is the user stopping; the gateway now owes them an answer.
	EvUserSpeechEnd
	// EvTurnAbort is the user abandoning the recording without ending it.
	EvTurnAbort
	// EvAIFirstOutput is the AI producing its first text or audio for this turn.
	EvAIFirstOutput
	// EvAIEnd is the AI's turn ending, whatever the outcome.
	EvAIEnd
)

func (e TurnEvent) String() string {
	switch e {
	case EvUserSpeechStart:
		return "user.speech.start"
	case EvUserSpeechEnd:
		return "user.speech.end"
	case EvTurnAbort:
		return "client.turn.abort"
	case EvAIFirstOutput:
		return "ai.first-output"
	case EvAIEnd:
		return "ai.turn.end"
	default:
		return "unknown"
	}
}

// turnTransitions is the whitelist of legal state changes.
//
// A whitelist rather than a blacklist of "impossible" cases: the interesting
// question is what a turn may do next, and every entry added here is a claim
// that the client can legitimately produce that sequence. Anything not listed is
// counted (see Turn.Rejected) rather than silently absorbed, because today a
// duplicate or out-of-order frame is swallowed by whichever branch runs and
// leaves no trace — and "how often does the client do that" is not answerable
// from the logs (docs/99_ D1).
var turnTransitions = map[TurnState]map[TurnEvent]TurnState{
	TurnIdle: {
		// A new utterance. Note there is no transition for AI output here: the
		// AI cannot speak before the user has, and if it appears to, something
		// upstream is confused.
		EvUserSpeechStart: TurnListening,
		// An end without a start. This is not a hypothetical sequence: a client
		// that sent no user.speech.start — because it did not observe one, or
		// because its capture began before the socket was ready — still ends an
		// utterance, and the end frame is itself proof the user spoke. Refusing
		// it would throw away the turn's name and leave the badge and the rescue
		// anchor unnamed.
		EvUserSpeechEnd: TurnFinalizing,
	},
	TurnListening: {
		EvUserSpeechEnd: TurnFinalizing,
		EvTurnAbort:     TurnClosed,
		// A second start without an end: iOS re-arms the recording. Treating it
		// as noise would lose the turn; staying in Listening is honest, since
		// the user is still the one talking.
		EvUserSpeechStart: TurnListening,
	},
	TurnFinalizing: {
		EvAIFirstOutput: TurnSpeaking,
		// The AI can end a turn without producing anything (a refused or empty
		// reply). The turn still closes.
		EvAIEnd: TurnClosed,
		// An abort can land after the end frame: the user hit stop while the
		// gateway was already waiting. The turn is over either way.
		EvTurnAbort:       TurnClosed,
		EvUserSpeechStart: TurnListening,
	},
	TurnSpeaking: {
		EvAIEnd:           TurnClosed,
		EvUserSpeechStart: TurnListening,
	},
	TurnClosed: {
		// The next utterance.
		EvUserSpeechStart: TurnListening,
	},
}

// Turn is one conversational turn and the state it is in.
//
// # What it owns
//
// The turn's identity, and the legal ordering of its events. Both used to be
// nobody's: the id was resolved in three places from two different fallbacks
// (session id in the handler, a counter in the provider), and the ordering was
// implicit in whichever branch happened to run.
type Turn struct {
	mu       sync.Mutex
	state    TurnState
	id       string
	session  string
	rejected map[TurnEvent]int
}

// NewTurn starts an idle turn for one client session. sessionID is the last
// resort for a turn the client never named.
func NewTurn(sessionID string) *Turn {
	return &Turn{
		state:    TurnIdle,
		session:  strings.TrimSpace(sessionID),
		rejected: map[TurnEvent]int{},
	}
}

// State reports the current state.
func (t *Turn) State() TurnState {
	if t == nil {
		return TurnIdle
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.state
}

// ID is this turn's identity, resolved once and then stable for the turn's life.
//
// Everything that needs to name the turn reads it from here — the badge, the
// rescue anchor, the ladder, the end-of-session report. Two places deriving the
// same name from the same event is how they come to disagree.
func (t *Turn) ID() string {
	if t == nil {
		return ""
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.id
}

// Apply advances the turn and reports whether the event was legal.
//
// A rejected event changes nothing and is counted. Callers are not expected to
// branch on the result: the gateway's job is to serve the learner, and refusing
// to process a frame because its ordering was unexpected would turn a client bug
// into a broken session. The count is what makes the bug visible.
func (t *Turn) Apply(ev TurnEvent) bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	next, ok := turnTransitions[t.state][ev]
	if !ok {
		t.rejected[ev]++
		return false
	}
	t.state = next
	return true
}

// ApplyStart records that the user began speaking.
//
// It deliberately does **not** name the turn. The client sends the turn id on
// user.speech.end, not on start (this is the same rule the provider follows when
// it stamps outbound frames), so a name taken here would have to be replaced
// later — and an id that changes mid-turn is worse than one that arrives late.
func (t *Turn) ApplyStart() bool {
	return t.Apply(EvUserSpeechStart)
}

// ApplySpeechEnd records that the user stopped, and names the turn.
//
// The name is resolved here and nowhere else: the client's id when it sent one —
// that is the id its own analytics use, and the id already on every frame the
// provider emitted for this turn — otherwise the session's, which is what the
// rest of the session's events are named after.
//
// One rule, one place. It used to be three: the badge, the rescue anchor, and
// the provider each decided separately, with the provider falling back to a
// counter of its own.
func (t *Turn) ApplySpeechEnd(clientTurnID string) bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	ok := true
	if next, allowed := turnTransitions[t.state][EvUserSpeechEnd]; allowed {
		t.state = next
	} else {
		t.rejected[EvUserSpeechEnd]++
		ok = false
	}
	// The name is recorded whether or not the ordering was expected. A name is
	// not a state: dropping it because the sequence surprised us would leave the
	// badge, the ladder and the end-of-session report describing the same turn by
	// different words — the exact failure this type exists to remove.
	if id := strings.TrimSpace(clientTurnID); id != "" {
		t.id = id
	} else if strings.TrimSpace(t.id) == "" {
		t.id = t.session
	}
	return ok
}

// NoteFirstOutput records that the AI started producing for this turn.
//
// The caller cannot know which frame was first. ai.text.delta and the binary
// audio frames both mean "the AI is speaking", they arrive interleaved from the
// provider, and the first of them differs per provider and per turn. So this is
// idempotent by design: once the turn is speaking, later output is accepted as
// more of the same utterance rather than counted.
//
// Counting them was the alternative, and it is wrong in a specific way: the
// rejections would be produced by the gateway's own frame stream, and the
// counter exists precisely to tell the client's behaviour apart from ours
// (docs/99_ D1).
//
// What is still counted is output from a turn that never began — an idle or
// already-closed turn producing AI frames is an anomaly worth seeing.
func (t *Turn) NoteFirstOutput() bool {
	if t == nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.state == TurnSpeaking {
		return true
	}
	next, allowed := turnTransitions[t.state][EvAIFirstOutput]
	if !allowed {
		t.rejected[EvAIFirstOutput]++
		return false
	}
	t.state = next
	return true
}

// NoteAIEnd records that the AI's turn ended, and reports whether the turn it
// closed was one the AI actually spoke in.
//
// The report is B8's arming rule, not a rejection count: a silence window may
// only open for a turn the AI took, and TurnSpeaking is what "took it" means
// here. Nothing else can answer that question — the wire says only that a turn
// ended, and a provider announces the session opening with the same frame it
// uses for a reply, so the frame alone cannot tell the two apart.
//
// The state is read *before* the transition, because closing the turn destroys
// the answer: TurnClosed is reached both by a reply that spoke and by an
// announcement that never did.
//
// Idempotent once closed — a single turn legitimately carries two end markers
// (ai.tts.end terminates the audio stream, ai.turn.end finalizes the item), and
// only the first is a transition. A repeat reports false: it is the same turn
// saying goodbye twice, and re-opening the window on it would restart the
// ladder's clock for no reason.
func (t *Turn) NoteAIEnd() (spoke bool) {
	if t == nil {
		return false
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.state == TurnClosed {
		return false
	}
	spoke = t.state == TurnSpeaking
	next, allowed := turnTransitions[t.state][EvAIEnd]
	if !allowed {
		t.rejected[EvAIEnd]++
		return false
	}
	t.state = next
	return spoke
}

// Rejected reports how many events of each kind were refused, for logging.
//
// A non-zero count is not an error to act on — it is the answer to "does the
// client ever do this", which used to require a packet capture.
func (t *Turn) Rejected() map[TurnEvent]int {
	if t == nil {
		return nil
	}
	t.mu.Lock()
	defer t.mu.Unlock()
	out := make(map[TurnEvent]int, len(t.rejected))
	for ev, n := range t.rejected {
		out[ev] = n
	}
	return out
}

// turnOutcomeSpoke reports whether an ai.turn.end outcome means the AI actually
// asked the user something.
//
// Every outcome closes the turn — a timeout ends it as surely as an answer does
// — but only some outcomes opened a silence worth rescuing. Rescue's window used
// to compare the outcome strings inline, which put "what counts as the AI having
// spoken" in two vocabularies at once: the state machine's and the outcome
// enum's. One function, used by both.
func turnOutcomeSpoke(outcome string) bool {
	switch outcome {
	case voiceproto.TurnOutcomeTimeout, voiceproto.TurnOutcomeError:
		return false
	default:
		return true
	}
}
