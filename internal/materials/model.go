// Package materials implements B21 A1/A2 material create + refine into phrase_blocks.
package materials

import "time"

const (
	// KindPaste is free-form pasted English.
	KindPaste = "paste"
	// KindSentence is a single workplace sentence.
	KindSentence = "sentence"
	// KindURL is a source URL; fetch is stubbed in V1.
	KindURL = "url"

	// StatusQueued means refine has not started.
	StatusQueued = "queued"
	// StatusProcessing means the LLM call is in flight.
	StatusProcessing = "processing"
	// StatusReady means refine finished (including zero extracted blocks).
	StatusReady = "ready"
	// StatusFailed means refine stopped with error_code.
	StatusFailed = "failed"

	// URLPlaceholder is stored instead of fetching remote HTML.
	URLPlaceholder = "[URL content placeholder]"
	// MaxContentLen is the POST /materials content byte cap.
	MaxContentLen = 5000

	// ErrorLLMTimeout is set when Completer fails or times out.
	ErrorLLMTimeout = "llm_timeout"
	// ErrorParse is set when the LLM payload is not refine JSON.
	ErrorParse = "parse_error"
	// ErrorDB is set when phrase_blocks write fails.
	ErrorDB = "db_error"
	// ErrorNoChunks is set on ready with zero usable blocks.
	ErrorNoChunks = "no_chunks_extracted"
)

// Material is one user-submitted source for phrase-block refine.
type Material struct {
	ID           string     `json:"id"`
	UserID       string     `json:"-"`
	Kind         string     `json:"kind"`
	Content      string     `json:"content"`
	RefineStatus string     `json:"refine_status"`
	BlockCount   int        `json:"block_count"`
	ErrorCode    string     `json:"error_code,omitempty"`
	DeletedAt    *time.Time `json:"-"`
	CreatedAt    time.Time  `json:"created_at"`
	UpdatedAt    time.Time  `json:"updated_at"`
}

// CreateRequest is POST /materials.
type CreateRequest struct {
	Kind    string `json:"kind"`
	Content string `json:"content"`
}

// CreateResponse is returned with HTTP 202.
type CreateResponse struct {
	MaterialID   string `json:"material_id"`
	RefineStatus string `json:"refine_status"`
}

// RefinedBlock is one LLM-extracted phrase block.
type RefinedBlock struct {
	IntentZH     string `json:"intent_zh"`
	ExpressionEN string `json:"expression_en"`
	SceneTag     string `json:"scene_tag"`
	FunctionTag  string `json:"function_tag"`
}

// RefineResult is the LLM JSON payload.
type RefineResult struct {
	Blocks []RefinedBlock `json:"blocks"`
}
