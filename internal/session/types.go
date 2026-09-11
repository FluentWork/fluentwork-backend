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
	JobTypeSessionEval     = "session.eval"

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
	// DeletedAt is A4 soft-delete. MySQL scanSession does not load this column.
	DeletedAt *time.Time
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
	LLMEvalJSON   []byte
	CreatedAt     time.Time
	// Interrupted marks a reply the user cut off. `Text` is then only the part
	// that had been delivered when they did — the transcript records what was
	// heard, not what was generated (77_ P1-14).
	Interrupted bool
}

// EndRequest is the body of POST /internal/v1/sessions/end.
type EndRequest struct {
	SessionID   string             `json:"session_id"`
	DurationSec int                `json:"duration_sec"`
	Reason      string             `json:"reason"`
	Utterances  []EndUtteranceItem `json:"utterances"`
	// VoiceUsage is what the gateway measured for this session. Absent when the
	// provider cannot report it (mock, dev-echo) — and absent means *no ledger
	// row*, not a row of zeroes. See buildVoiceCostLog.
	VoiceUsage *VoiceUsageItem `json:"voice_usage,omitempty"`
}

// VoiceUsageItem is the voice path's contribution to cost accounting, as the
// gateway measured it.
//
// Milliseconds per direction rather than one total, because the two directions
// are not priced alike and the split cannot be recovered later. The ledger
// currently stores only their sum in `audio_sec`; see buildVoiceCostLog for why
// that is a known limitation rather than a design.
type VoiceUsageItem struct {
	UplinkMS   int64  `json:"uplink_ms"`
	DownlinkMS int64  `json:"downlink_ms"`
	Model      string `json:"model,omitempty"`
}

// EndUtteranceItem is a transcript turn submitted at session end.
type EndUtteranceItem struct {
	Seq     int    `json:"seq"`
	Speaker string `json:"speaker"`
	Text    string `json:"text"`
	// Interrupted is set by the gateway when the user barged in mid-reply.
	Interrupted bool `json:"interrupted,omitempty"`
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

// ContinuationContextRequest is the body of
// POST /internal/v1/sessions/continuation-context.
//
// Both ids are required and that is the point of the endpoint: the previous
// session's transcript may only be handed to a session owned by the same user,
// and the app-server is the only side that knows who owns what. The gateway
// knows one of these ids from a ticket it issued and the other from a frame the
// client wrote, so it cannot make this call on its own.
type ContinuationContextRequest struct {
	CurrentSessionID  string `json:"current_session_id"`
	PreviousSessionID string `json:"previous_session_id"`
	Limit             int    `json:"limit"`
}

// ContinuationContextResponse carries the tail of the previous transcript,
// oldest first. An empty list is a normal answer — the previous session may
// have had nothing said in it — and is not distinguished from one the gateway
// should ignore.
type ContinuationContextResponse struct {
	Utterances []ContinuationUtterance `json:"utterances"`
}

// ContinuationUtterance is one turn of a previous session, for seeding a new
// one. Deliberately not `Utterance`: that type carries row ids, interrupted
// flags and eval payloads, none of which the provider has any use for, and
// keeping them apart means a column added there cannot silently widen what is
// sent to a model.
type ContinuationUtterance struct {
	Seq     int    `json:"seq"`
	Speaker string `json:"speaker"`
	Text    string `json:"text"`
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
	// Eval is the B18 three-dimension summary when per-utterance scoring is complete.
	Eval *EvalSummary `json:"eval,omitempty"`
}

// EvalDims is grammar / fluency / vocabulary on 0–1.
type EvalDims struct {
	Grammar    float64 `json:"grammar"`
	Fluency    float64 `json:"fluency"`
	Vocabulary float64 `json:"vocabulary"`
}

// EvalSummary is the session-level B18 review eval payload.
type EvalSummary struct {
	Score       float64  `json:"score"`
	Dims        EvalDims `json:"dims"`
	Suggestions []string `json:"suggestions"`
	UtteranceN  int      `json:"utterance_count"`
	Complete    bool     `json:"complete"`
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
