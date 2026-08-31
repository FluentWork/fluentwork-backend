// Package session implements practice session creation and WSS ticket issuance (B2).
//
// POST /api/v1/sessions creates a practice_sessions row and returns
// session_id + wss_url + a one-time ticket (default TTL 60s) for voice-gateway.
package session

import (
	"encoding/json"
	"time"
)

// Session statuses for the first-wave session lifecycle.
const (
	StatusCreated   = "created"
	StatusActive    = "active"
	StatusEnded     = "ended"
	StatusAbandoned = "abandoned"
	StatusReviewed  = "reviewed"
)

// DefaultSceneType is used when the client omits scene_type.
const DefaultSceneType = "demo"

// Job types and statuses for the async review outbox (B5).
const (
	JobTypeSessionFinished = "session.finished"

	JobStatusPending    = "pending"
	JobStatusProcessing = "processing"
	JobStatusDone       = "done"
	JobStatusFailed     = "failed"
)

// MaxJobAttempts is initial try + one retry (backend tech design §5.2).
const MaxJobAttempts = 2

// DefaultJobLease is how long a claimed job may stay in processing before
// another worker may reclaim it (crash / hung worker recovery).
const DefaultJobLease = 2 * time.Minute

// DefaultJobTimeout bounds a single job's runJob work (not Fail/Complete).
const DefaultJobTimeout = 60 * time.Second

// Session is the practice_sessions aggregate.
type Session struct {
	ID          string
	UserID      string
	MaterialID  *string
	SceneType   string
	Status      string
	DurationSec int
	ReviewJSON  []byte
	CreatedAt   time.Time
	UpdatedAt   time.Time
}

// Job is an outbox row consumed by the review worker.
type Job struct {
	ID          string
	SessionID   string
	JobType     string
	Status      string
	Attempts    int
	AvailableAt time.Time
	LockedAt    *time.Time
	LockedBy    *string
	LastError   *string
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

// Review poll statuses for GET /sessions/:id/review (B6).
const (
	ReviewPollPending = "pending"
	ReviewPollReady   = "ready"
	ReviewPollFailed  = "failed"
)

// ReviewPollResponse is returned by GET /sessions/:id/review.
type ReviewPollResponse struct {
	SessionID string `json:"session_id"`
	Status    string `json:"status"`
	// Review is the canonical review_json document when Status is ready.
	// Since B9-R1/R2 it includes transcript + presentation slices + raw review/refine:
	// {generator,status,duration_sec,transcript,overview,evaluation,dual_column,refine_cards,review,refine}.
	Review json.RawMessage `json:"review,omitempty"`
}

// MessageChannelText is the only channel accepted by POST /sessions/:id/messages (B7).
const MessageChannelText = "text"

// PostMessageRequest is the body of POST /sessions/:id/messages.
type PostMessageRequest struct {
	Text string `json:"text"`
	// Channel must be "text" for the degraded path; other/empty values mean voice is preferred → 409.
	Channel string `json:"channel"`
}

// PostMessageResponse is the stub AI reply for text degrade mode.
type PostMessageResponse struct {
	SessionID string `json:"session_id"`
	Reply     string `json:"reply"`
	Channel   string `json:"channel"`
	Generator string `json:"generator"`
}
