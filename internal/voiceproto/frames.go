// Package voiceproto defines FluentWork voice-gateway WSS control frames (B3).
package voiceproto

import (
	"encoding/binary"
	"encoding/json"
	"fmt"
	"strings"
)

// Control frame type constants (shared with iOS / contract tests).
const (
	TypeAuth                   = "auth"
	TypeSessionReady           = "session.ready"
	TypeSessionStart           = "session.start"
	TypeUserSpeechStart        = "user.speech.start"
	TypeUserSpeechEnd          = "user.speech.end"
	TypeClientTurnAbort        = "client.turn.abort"
	TypeClientRescueRequest    = "client.rescue.request"
	TypeClientASRTranscription = "client.asr.transcription"
	TypeAITextDelta            = "ai.text.delta"
	TypeAIAudioChunk           = "ai.audio.chunk"
	TypeAITTSStart             = "ai.tts.start"
	TypeAITTSAudio             = "ai.tts.audio"
	TypeAITTSEnd               = "ai.tts.end"
	TypeAITurnEnd              = "ai.turn.end"
	TypeInterrupt              = "interrupt"
	TypeFeedbackBadge          = "feedback.badge"
	TypeSessionEnd             = "session.end"
	TypeError                  = "error"
	TypePong                   = "pong"
	TypePing                   = "ping"
)

// Envelope is a partially decoded control frame used for type dispatch.
type Envelope struct {
	Type string `json:"type"`
}

// Auth is the first client→gateway frame after WSS upgrade.
type Auth struct {
	Type   string `json:"type"`
	Ticket string `json:"ticket"`
}

// SessionReady is sent by the gateway after a ticket is consumed.
type SessionReady struct {
	Type      string `json:"type"`
	SessionID string `json:"session_id"`
	UserID    string `json:"user_id,omitempty"`
}

// SessionStart begins the voice practice loop (B3 accepts; vendor fan-out later).
type SessionStart struct {
	Type       string `json:"type"`
	MaterialID string `json:"material_id,omitempty"`
	SceneType  string `json:"scene_type,omitempty"`
	Voice      string `json:"voice,omitempty"`
	// ContinueFromSessionID asks for this session to open with the tail of an
	// earlier one, so "continue where we left off" does not start from zero.
	//
	// It is an **id, not content**: the client is not allowed to write the
	// provider's instructions, and the transcript it names belongs to a user
	// who has to be checked. Both of those are the app-server's to enforce, so
	// the gateway resolves this through an internal call before opening the
	// provider, and an id that fails to resolve leaves the session exactly as
	// it would have been otherwise.
	ContinueFromSessionID string `json:"continue_from_session_id,omitempty"`
}

// SessionEnd closes the voice session from the client.
type SessionEnd struct {
	Type   string `json:"type"`
	Reason string `json:"reason,omitempty"`
}

// UserSpeechStart opens a user-speech window (C→S). No turn_id — the
// turn is identified on user.speech.end or client.turn.abort.
type UserSpeechStart struct {
	Type string `json:"type"`
}

// UserSpeechEnd is the client→gateway end-of-utterance signal.
//
// Text is an optional client ASR transcript used by B7 hit-detection (B12).
// When empty, the gateway simply skips hit-detection for this turn — omitting
// the field is the cheapest "no extra payload" path for clients that perform
// ASR server-side.
//
// TurnID is an optional per-utterance identifier that becomes the dedupe key
// component for emitted feedback.badge frames. When empty, the gateway falls
// back to using SessionID as the turn scope, which suppresses all repeats of
// the same phrase block across the whole session (acceptable for short
// sessions; long sessions should populate this field).
type UserSpeechEnd struct {
	Type   string `json:"type"`
	Text   string `json:"text,omitempty"`
	TurnID string `json:"turn_id,omitempty"`
}

// ClientTurnAbort outcomes (C→S). Subset of TurnOutcome: abort is never "ok".
const (
	ClientTurnAbortTimeout       = "timeout"
	ClientTurnAbortUserAbandoned = "user_abandoned"
	ClientTurnAbortError         = "error"
)

// ValidClientTurnAbortOutcome reports whether outcome is a legal abort reason.
func ValidClientTurnAbortOutcome(outcome string) bool {
	switch strings.TrimSpace(outcome) {
	case ClientTurnAbortTimeout, ClientTurnAbortUserAbandoned, ClientTurnAbortError:
		return true
	default:
		return false
	}
}

// ClientTurnAbort is the client→gateway cancel of an in-progress recording
// turn (I20 T-I20-1). Distinct from B15: this fires before user.speech.end,
// must not start collectTurn, and must not close the WSS session.
//
// Outcome is required: timeout | user_abandoned | error. Session stays alive;
// the next user.speech.start begins a new turn. session_id is omitted — the
// session is the WSS connection, same as user.speech.end.
type ClientTurnAbort struct {
	Type    string `json:"type"`
	TurnID  string `json:"turn_id,omitempty"`
	Outcome string `json:"outcome"`
}

// ClientRescueRequest is the client→gateway ask for one rung of the B8 rescue
// ladder (C→S).
//
// It carries no level. The ladder's position is the gateway's, because a client
// that could name one could ask for the worked example on its first tap, and
// §5.4's ladder climbs one rung at a time. It shares the automatic ladder's
// position for the same reason in reverse: a tap advances that ladder rather
// than opening a second one, so a tap and the 3s trigger can never both answer
// the same silence.
type ClientRescueRequest struct {
	Type string `json:"type"`
}

// ClientASRTranscription is a gateway→client frame emitted when the voice
// provider (e.g., Volcengine Duplex) returns an ASR transcription of the
// user's audio turn (B14).
//
// The transcript is authoritative — it comes from the same real-time stream that
// drives the AI response, so it is always consistent with what the model heard.
// Clients that previously ran on-device ASR (Apple Speech) should consume this
// frame and skip their local transcriber to avoid double-transcription and
// inconsistent results.
//
// Fields:
//   - Text:   the full transcribed text of the user's audio
//   - TurnID: correlates this transcription with the originating speech turn
type ClientASRTranscription struct {
	Type   string `json:"type"`
	Text   string `json:"text"`
	TurnID string `json:"turn_id,omitempty"`
}

// AITextDelta is a gateway→client incremental assistant text frame (v2).
// ServerTsMs is UTC Unix milliseconds taken at serialize time. It is optional
// on the wire; iOS must still accept frames without it. Clock offset is
// estimated from ping RTT, not from assuming NTP on the phone.
type AITextDelta struct {
	Type       string `json:"type"`
	Text       string `json:"text"`
	TurnID     string `json:"turn_id,omitempty"`
	ServerTsMs int64  `json:"server_ts_ms,omitempty"`
}

// NewAITextDelta builds a v2 text delta. serverTsMs is Unix milliseconds.
func NewAITextDelta(text, turnID string, serverTsMs int64) AITextDelta {
	return AITextDelta{
		Type:       TypeAITextDelta,
		Text:       text,
		TurnID:     strings.TrimSpace(turnID),
		ServerTsMs: serverTsMs,
	}
}

// AITTSStart warms the client decoder before binary TTS audio (WSS V2).
type AITTSStart struct {
	Type       string  `json:"type"`
	TurnID     string  `json:"turn_id"`
	VoiceID    string  `json:"voice_id"`
	SampleRate int     `json:"sample_rate"`
	Codec      string  `json:"codec"`
	TurnRef    *uint32 `json:"turn_ref,omitempty"`
}

// AITTSAudio is a WebSocket binary TTS frame, not JSON.
// Wire layout matches iOS WSAudioFrameCodec: 4-byte big-endian seq + payload.
type AITTSAudio struct {
	Seq     uint32
	TurnRef *uint32
	Payload []byte
}

// AITTSEnd terminates the TTS stream after the last binary audio message.
type AITTSEnd struct {
	Type             string  `json:"type"`
	TurnID           string  `json:"turn_id"`
	CompletionStatus string  `json:"completion_status"`
	DurationMs       *int    `json:"duration_ms,omitempty"`
	TurnRef          *uint32 `json:"turn_ref,omitempty"`
}

// AudioFrameLayout is the header shape of one downlink binary audio message.
type AudioFrameLayout uint8

const (
	// AudioFrameLayoutH4 is [4-byte seq][payload].
	AudioFrameLayoutH4 AudioFrameLayout = iota
	// AudioFrameLayoutH8 is [4-byte seq][4-byte turn_ref][payload].
	AudioFrameLayoutH8
)

// HeaderBytes is how many bytes precede the payload in this layout.
func (l AudioFrameLayout) HeaderBytes() int {
	if l == AudioFrameLayoutH8 {
		return 8
	}
	return 4
}

// String names this layout the way the frozen schema does.
func (l AudioFrameLayout) String() string {
	if l == AudioFrameLayoutH8 {
		return "h8"
	}
	return "h4"
}

// AudioFrameLayoutFor selects the layout a turn's binary frames use.
func AudioFrameLayoutFor(turnRef *uint32) AudioFrameLayout {
	if turnRef == nil {
		return AudioFrameLayoutH4
	}
	return AudioFrameLayoutH8
}

// Encode writes the frozen binary ai.tts.audio layout. Payload must be non-empty.
func (f AITTSAudio) Encode() ([]byte, error) {
	if len(f.Payload) == 0 {
		return nil, fmt.Errorf("ai.tts.audio payload must be non-empty")
	}
	layout := AudioFrameLayoutFor(f.TurnRef)
	header := layout.HeaderBytes()
	out := make([]byte, header+len(f.Payload))
	binary.BigEndian.PutUint32(out[:4], f.Seq)
	if layout == AudioFrameLayoutH8 {
		binary.BigEndian.PutUint32(out[4:8], *f.TurnRef)
	}
	copy(out[header:], f.Payload)
	return out, nil
}

// DecodeAITTSAudio parses a WebSocket binary TTS message.
func DecodeAITTSAudio(raw []byte, layout AudioFrameLayout) (AITTSAudio, error) {
	header := layout.HeaderBytes()
	if len(raw) < header+1 {
		return AITTSAudio{}, fmt.Errorf("ai.tts.audio truncated: %d bytes, need at least %d", len(raw), header+1)
	}
	frame := AITTSAudio{
		Seq:     binary.BigEndian.Uint32(raw[:4]),
		Payload: append([]byte(nil), raw[header:]...),
	}
	if layout == AudioFrameLayoutH8 {
		turnRef := binary.BigEndian.Uint32(raw[4:8])
		frame.TurnRef = &turnRef
	}
	return frame, nil
}

// Turn outcomes carried on ai.turn.end (S→C).
//
// They mirror voicepoc.TurnOutcome 1:1, and are declared here rather than
// imported from there because this is the package that owns the wire value: a
// consumer that only reads the frame — B8's rescue window, which must not arm a
// ladder against a question that was never asked — has no business depending on
// the vendor POC layer to spell "timeout".
const (
	TurnOutcomeOK      = "ok"
	TurnOutcomePartial = "partial"
	TurnOutcomeTimeout = "timeout"
	TurnOutcomeError   = "error"
)

// AITurnEnd marks the explicit end boundary of one assistant turn.
type AITurnEnd struct {
	Type   string `json:"type"`
	TurnID string `json:"turn_id,omitempty"`
	// B15: explicit terminal status so iOS can distinguish ok/partial/timeout/error
	// without relying on implicit timing heuristics. Maps 1:1 to voicepoc.TurnOutcome.
	Outcome string `json:"outcome,omitempty"` // "" | TurnOutcome* above
	// B15-I3: vendor log_id from Volcengine handshake (X-Tt-Logid) for cross-layer trace.
	// Allows iOS tracker events to be correlated with vendor-side diagnostic logs.
	LogID string `json:"log_id,omitempty"`
}

// Interrupt asks the gateway/vendor path to stop AI audio.
type Interrupt struct {
	Type   string `json:"type"`
	MaxSeq *int64 `json:"max_seq,omitempty"`
}

// ErrorFrame is a gateway→client error notice.
type ErrorFrame struct {
	Type    string `json:"type"`
	Code    string `json:"code"`
	Message string `json:"message"`
}

// Pong answers a client ping.
type Pong struct {
	Type string `json:"type"`
	TS   int64  `json:"ts,omitempty"`
}

// Ping is an optional client keepalive.
type Ping struct {
	Type string `json:"type"`
	TS   int64  `json:"ts,omitempty"`
}

// FeedbackBadgeTier classifies the badge display intensity (B12).
const (
	BadgeTierSoft      = "soft"
	BadgeTierHighlight = "highlight"
	BadgeTierCelebrate = "celebrate"
)

// FeedbackBadge is gateway→client when a user's spoken phrase matches a
// stored phrase block from the corpus (B12 B7 hit-detection path).
//
// Required: Badge (displayed label).
// Optional context: PhraseBlockID (corpus link), SessionID / TurnID (for
// upstream correlation), Tier (display intensity), DedupeKey (caller-computed
// key used by the gateway to suppress duplicate frames within one turn).
type FeedbackBadge struct {
	Type          string `json:"type"`
	Badge         string `json:"badge"`
	PhraseBlockID string `json:"phrase_block_id,omitempty"`
	Tier          string `json:"tier,omitempty"`
	SessionID     string `json:"session_id,omitempty"`
	TurnID        string `json:"turn_id,omitempty"`
	DedupeKey     string `json:"dedupe_key,omitempty"`
}

// NewFeedbackBadge builds a FeedbackBadge with the canonical dedupe key.
// sessionID and turnID are required for any badge the gateway emits — callers
// that lack either must skip emitting rather than fabricate identifiers.
func NewFeedbackBadge(badge, phraseBlockID, tier, sessionID, turnID string) FeedbackBadge {
	return FeedbackBadge{
		Type:          TypeFeedbackBadge,
		Badge:         badge,
		PhraseBlockID: phraseBlockID,
		Tier:          tier,
		SessionID:     sessionID,
		TurnID:        turnID,
		DedupeKey:     ComposeBadgeDedupeKey(sessionID, turnID, phraseBlockID),
	}
}

// ComposeBadgeDedupeKey is the canonical key the gateway uses to suppress
// duplicate feedback.badge frames for the same (session, turn, phrase_block).
// Returns "" when any required field is missing — callers must treat that
// as "do not emit a dedupable badge" rather than fabricate a key.
func ComposeBadgeDedupeKey(sessionID, turnID, phraseBlockID string) string {
	s := strings.TrimSpace(sessionID)
	t := strings.TrimSpace(turnID)
	p := strings.TrimSpace(phraseBlockID)
	if s == "" || t == "" || p == "" {
		return ""
	}
	return s + "|" + t + "|" + p
}

// DecodeType returns the frame type from raw JSON.
func DecodeType(raw []byte) (string, error) {
	var env Envelope
	if err := json.Unmarshal(raw, &env); err != nil {
		return "", fmt.Errorf("decode envelope: %w", err)
	}
	if env.Type == "" {
		return "", fmt.Errorf("missing type")
	}
	return env.Type, nil
}

// MustMarshal JSON-encodes v or panics (tests / static fixtures only).
func MustMarshal(v any) []byte {
	b, err := json.Marshal(v)
	if err != nil {
		panic(err)
	}
	return b
}
