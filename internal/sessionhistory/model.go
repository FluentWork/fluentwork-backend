// Package sessionhistory implements B24 GET /sessions list and detail.
package sessionhistory

import "time"

const (
	// DefaultPageSize is GET /sessions size when omitted or out of range.
	DefaultPageSize = 20
	// MaxPageSize is the documented cap; values above it fall back to DefaultPageSize.
	MaxPageSize = 100
)

// SessionListItem is one row in GET /sessions.
type SessionListItem struct {
	SessionID   string    `json:"session_id"`
	SceneType   string    `json:"scene_type"`
	Status      string    `json:"status"`
	StartedAt   time.Time `json:"started_at"`
	DurationSec int       `json:"duration_sec"`
	MaterialID  *string   `json:"material_id,omitempty"`
}

// SessionListPage is the cursor page for GET /sessions.
type SessionListPage struct {
	Items      []SessionListItem `json:"items"`
	NextCursor *string           `json:"next_cursor,omitempty"`
	Size       int               `json:"size"`
}

// SessionDetail is GET /sessions/:id.
type SessionDetail struct {
	SessionID   string          `json:"session_id"`
	SceneType   string          `json:"scene_type"`
	Status      string          `json:"status"`
	StartedAt   time.Time       `json:"started_at"`
	DurationSec int             `json:"duration_sec"`
	MaterialID  *string         `json:"material_id,omitempty"`
	Materials   []MaterialRef   `json:"materials"`
	Utterances  []UtteranceView `json:"utterances"`
	Review      *DetailReview   `json:"review,omitempty"`
}

// MaterialRef is a B21 placeholder; empty until the materials module lands.
type MaterialRef struct {
	ID string `json:"id"`
}

// UtteranceView is one transcript turn in session detail.
type UtteranceView struct {
	Seq     int    `json:"seq"`
	Speaker string `json:"speaker"`
	Text    string `json:"text"`
}

// DetailReview is the B18 eval embed on session detail.
type DetailReview struct {
	Status      string   `json:"status,omitempty"`
	Score       float64  `json:"score"`
	Dims        *Dims    `json:"dims,omitempty"`
	Suggestions []string `json:"suggestions,omitempty"`
}

// Dims is grammar / fluency / vocabulary on 0–1.
type Dims struct {
	Grammar    float64 `json:"grammar"`
	Fluency    float64 `json:"fluency"`
	Vocabulary float64 `json:"vocabulary"`
}

// Cursor is the opaque pagination token payload.
type Cursor struct {
	StartedAt time.Time `json:"started_at"`
	ID        string    `json:"id"`
}
