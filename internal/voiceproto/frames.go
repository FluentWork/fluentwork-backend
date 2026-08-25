// Package voiceproto defines FluentWork voice-gateway WSS control frames (B3).
package voiceproto

import (
	"encoding/json"
	"fmt"
)

// Control frame type constants (shared with iOS / contract tests).
const (
	TypeAuth            = "auth"
	TypeSessionReady    = "session.ready"
	TypeSessionStart    = "session.start"
	TypeUserSpeechStart = "user.speech.start"
	TypeUserSpeechEnd   = "user.speech.end"
	TypeAITextDelta     = "ai.text.delta"
	TypeAIAudioChunk    = "ai.audio.chunk"
	TypeInterrupt       = "interrupt"
	TypeFeedbackBadge   = "feedback.badge"
	TypeSessionEnd      = "session.end"
	TypeError           = "error"
	TypePong            = "pong"
	TypePing            = "ping"
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
