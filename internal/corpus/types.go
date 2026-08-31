package corpus

import "time"

const (
	// StateNew marks a newly accepted phrase block.
	StateNew = "new"
	// StateTraining marks a block in active spaced repetition.
	StateTraining = "training"
	// StateAutomated marks a block that graduated to low-touch review.
	StateAutomated = "automated"
)

var validStates = map[string]struct{}{
	StateNew:       {},
	StateTraining:  {},
	StateAutomated: {},
}

var validSceneTags = map[string]struct{}{
	"standup":   {},
	"review":    {},
	"1on1":      {},
	"interview": {},
	"casual":    {},
}

var validFunctionTags = map[string]struct{}{
	"object":    {},
	"clarify":   {},
	"report":    {},
	"propose":   {},
	"agree":     {},
	"disagree":  {},
	"ask":       {},
	"summarize": {},
	"defer":     {},
	"commit":    {},
}

// PhraseBlock is the persisted refine/corpus row for one user expression.
type PhraseBlock struct {
	ID              string
	UserID          string
	IntentZH        string
	ExpressionEN    string
	AnchorUserSaid  string
	SceneTag        string
	FunctionTag     string
	State           string
	SuccessStreak   int
	NextDueAt       time.Time
	EaseFactor      float64
	RealUseCount    int
	IsFavorite      bool
	PinnedAt        *time.Time
	SourceSessionID *string
	DeletedAt       *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// ListBlocksRequest is the service input for paginated corpus queries.
type ListBlocksRequest struct {
	UserID       string
	SceneTag     string
	FunctionTag  string
	Keyword      string
	Cursor       string
	UpdatedAfter string
	Limit        int
	FavoriteOnly bool
}

// ListBlocksResponse is the paginated corpus list returned to clients.
type ListBlocksResponse struct {
	Items       []PhraseBlockView `json:"items"`
	NextCursor  string            `json:"next_cursor,omitempty"`
	CursorReset bool              `json:"cursor_reset"`
}

// PhraseBlockView is the API projection of one phrase block.
type PhraseBlockView struct {
	ID              string     `json:"id"`
	IntentZH        string     `json:"intent_zh"`
	ExpressionEN    string     `json:"expression_en"`
	AnchorUserSaid  string     `json:"anchor_user_said"`
	SceneTag        string     `json:"scene_tag"`
	FunctionTag     string     `json:"function_tag"`
	State           string     `json:"state"`
	SuccessStreak   int        `json:"success_streak"`
	NextDueAt       time.Time  `json:"next_due_at"`
	EaseFactor      float64    `json:"ease_factor"`
	RealUseCount    int        `json:"real_use_count"`
	IsFavorite      bool       `json:"is_favorite"`
	PinnedAt        *time.Time `json:"pinned_at,omitempty"`
	SourceSessionID *string    `json:"source_session_id,omitempty"`
	DeletedAt       *time.Time `json:"deleted_at,omitempty"`
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

// UpdateBlockRequest carries editable phrase block fields.
type UpdateBlockRequest struct {
	IntentZH       string `json:"intent_zh"`
	ExpressionEN   string `json:"expression_en"`
	AnchorUserSaid string `json:"anchor_user_said"`
	SceneTag       string `json:"scene_tag"`
	FunctionTag    string `json:"function_tag"`
}

// FavoriteBlockRequest toggles favorite/pinned state for one block.
type FavoriteBlockRequest struct {
	IsFavorite bool `json:"is_favorite"`
	Pinned     bool `json:"pinned"`
}

// BatchAcceptRequest accepts refine blocks from one review session.
type BatchAcceptRequest struct {
	SourceSessionID string             `json:"source_session_id"`
	Blocks          []BatchAcceptBlock `json:"blocks"`
}

// BatchAcceptBlock is one refine candidate accepted into the corpus.
type BatchAcceptBlock struct {
	IntentZH       string `json:"intent_zh"`
	ExpressionEN   string `json:"expression_en"`
	AnchorUserSaid string `json:"anchor_user_said"`
	SceneTag       string `json:"scene_tag"`
	FunctionTag    string `json:"function_tag"`
}

// BatchAcceptResponse reports how many blocks were accepted.
type BatchAcceptResponse struct {
	AcceptedCount int               `json:"accepted_count"`
	Items         []PhraseBlockView `json:"items"`
}
