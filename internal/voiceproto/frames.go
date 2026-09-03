// Package voiceproto defines FluentWork voice-gateway WSS control frames (B3).
package voiceproto

import (
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
	TypeClientASRTranscription = "client.asr.transcription"
	TypeAITextDelta            = "ai.text.delta"
	TypeAIAudioChunk           = "ai.audio.chunk"
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
}

// SessionEnd closes the voice session from the client.
type SessionEnd struct {
	Type   string `json:"type"`
	Reason string `json:"reason,omitempty"`
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

// AITurnEnd marks the explicit end boundary of one assistant turn.
type AITurnEnd struct {
	Type    string `json:"type"`
	TurnID  string `json:"turn_id,omitempty"`
	// B15: explicit terminal status so iOS can distinguish ok/partial/timeout/error
	// without relying on implicit timing heuristics. Maps 1:1 to voicepoc.TurnOutcome.
	Outcome string `json:"outcome,omitempty"` // "" | "ok" | "partial" | "timeout" | "error"
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
