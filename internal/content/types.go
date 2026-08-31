// Package content serves daily reads and related generated content APIs.
package content

import (
	"encoding/json"
	"time"
)

const (
	// StatusPending marks a daily read awaiting generation.
	StatusPending = "pending"
	// StatusReady marks a generated daily read available to the client.
	StatusReady = "ready"
	// StatusFailed marks a daily read that could not be generated.
	StatusFailed = "failed"
)

const (
	// GeneratorPreset is the static fallback generator name.
	GeneratorPreset = "preset-v1"
	// GeneratorCorpusStub builds daily reads from phrase blocks.
	GeneratorCorpusStub = "corpus-stub-v1"
	// GeneratorFollowRead acknowledges follow-read submissions without scoring.
	GeneratorFollowRead = "follow-read-stub-v1"
	// GeneratorArkDailyRead is reserved for the future Ark-backed generator.
	GeneratorArkDailyRead = "ark-daily-read-v1"
)

// DailyRead is one generated daily read row.
type DailyRead struct {
	ID           string
	UserID       string
	GenDate      time.Time
	Status       string
	Title        string
	Body         string
	AudioURL     *string
	UsedBlockIDs json.RawMessage
	SourceRefs   json.RawMessage
	ReadScore    *float64
	Generator    string
	CreatedAt    time.Time
	UpdatedAt    time.Time
}

// TodayPollResponse is returned by GET /daily-reads/today.
type TodayPollResponse struct {
	GenDate   string         `json:"gen_date"`
	Status    string         `json:"status"`
	DailyRead *DailyReadView `json:"daily_read,omitempty"`
}

// DailyReadView is the client-facing daily read payload.
type DailyReadView struct {
	ID           string          `json:"id"`
	Title        string          `json:"title"`
	Body         string          `json:"body"`
	AudioURL     *string         `json:"audio_url,omitempty"`
	Generator    string          `json:"generator"`
	UsedBlockIDs json.RawMessage `json:"used_block_ids,omitempty"`
	SourceRefs   json.RawMessage `json:"source_refs,omitempty"`
	ReadScore    *float64        `json:"read_score"`
}

// FollowReadRequest is the body of POST /daily-reads/:id/follow-read.
type FollowReadRequest struct {
	AudioURL *string `json:"audio_url"`
}

// FollowReadResponse acknowledges a follow-read submission without scoring.
type FollowReadResponse struct {
	DailyReadID string   `json:"daily_read_id"`
	Recorded    bool     `json:"recorded"`
	Score       *float64 `json:"read_score"`
	Generator   string   `json:"generator"`
}
