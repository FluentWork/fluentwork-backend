// Package session implements practice session creation and WSS ticket issuance (B2).
//
// POST /api/v1/sessions creates a practice_sessions row and returns
// session_id + wss_url + a one-time ticket (default TTL 60s) for voice-gateway.
package session

import "time"

// Session statuses for the first-wave session lifecycle.
const (
	StatusCreated   = "created"
	StatusActive    = "active"
	StatusEnded     = "ended"
	StatusAbandoned = "abandoned"
)

// DefaultSceneType is used when the client omits scene_type.
const DefaultSceneType = "demo"

// Session is the practice_sessions aggregate.
type Session struct {
	ID          string
	UserID      string
	MaterialID  *string
	SceneType   string
	Status      string
	DurationSec int
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Ticket is a one-time WSS credential bound to a session.
type Ticket struct {
	ID        string
	SessionID string
	UserID    string
	Hash      string
	ExpiresAt time.Time
	UsedAt    *time.Time
	CreatedAt time.Time
}

// Utterance speakers for first-wave transcript rows.
const (
	SpeakerUser = "user"
	SpeakerAI   = "ai"
)

// Utterance is one turn of a practice session transcript.
type Utterance struct {
	ID            string
	SessionID     string
	Seq           int
	Speaker       string
	Text          string
	ASRConfidence *float64
	AudioURL      *string
	CreatedAt     time.Time
}

// EndRequest is the body of POST /internal/v1/sessions/end.
type EndRequest struct {
	SessionID   string             `json:"session_id"`
	DurationSec int                `json:"duration_sec"`
	Reason      string             `json:"reason"`
	Utterances  []EndUtteranceItem `json:"utterances"`
}

// EndUtteranceItem is a transcript turn submitted at session end.
type EndUtteranceItem struct {
	Seq     int    `json:"seq"`
	Speaker string `json:"speaker"`
	Text    string `json:"text"`
}

// EndResponse is returned after a session is ended (or already ended).
type EndResponse struct {
	SessionID      string `json:"session_id"`
	Status         string `json:"status"`
	DurationSec    int    `json:"duration_sec"`
	UtteranceCount int    `json:"utterance_count"`
	AlreadyEnded   bool   `json:"already_ended,omitempty"`
}

// ActivateRequest is the body of POST /internal/v1/sessions/activate.
type ActivateRequest struct {
	SessionID string `json:"session_id"`
}

// ActivateResponse is returned after a session is marked active.
type ActivateResponse struct {
	SessionID string `json:"session_id"`
	Status    string `json:"status"`
}

// CreateRequest is the body of POST /sessions.
type CreateRequest struct {
	MaterialID *string `json:"material_id"`
	SceneType  string  `json:"scene_type"`
}

// CreateResponse is returned by POST /sessions.
type CreateResponse struct {
	SessionID       string    `json:"session_id"`
	WSSURL          string    `json:"wss_url"`
	Ticket          string    `json:"ticket"`
	TicketExpiresIn int64     `json:"ticket_expires_in"`
	TicketExpiresAt time.Time `json:"ticket_expires_at"`
	SceneType       string    `json:"scene_type"`
	Status          string    `json:"status"`
}
