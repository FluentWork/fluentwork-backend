package corpus

import "time"

const (
	StateNew       = "new"
	StateTraining  = "training"
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

type ListBlocksRequest struct {
	UserID       string
	SceneTag     string
	FunctionTag  string
	Keyword      string
	Cursor       string
	Limit        int
	FavoriteOnly bool
}

type ListBlocksResponse struct {
	Items      []PhraseBlockView `json:"items"`
	NextCursor string            `json:"next_cursor,omitempty"`
}

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
	CreatedAt       time.Time  `json:"created_at"`
	UpdatedAt       time.Time  `json:"updated_at"`
}

type UpdateBlockRequest struct {
	IntentZH       string `json:"intent_zh"`
	ExpressionEN   string `json:"expression_en"`
	AnchorUserSaid string `json:"anchor_user_said"`
	SceneTag       string `json:"scene_tag"`
	FunctionTag    string `json:"function_tag"`
}

type FavoriteBlockRequest struct {
	IsFavorite bool `json:"is_favorite"`
	Pinned     bool `json:"pinned"`
}

type BatchAcceptRequest struct {
	SourceSessionID string             `json:"source_session_id"`
	Blocks          []BatchAcceptBlock `json:"blocks"`
}

type BatchAcceptBlock struct {
	IntentZH       string `json:"intent_zh"`
	ExpressionEN   string `json:"expression_en"`
	AnchorUserSaid string `json:"anchor_user_said"`
	SceneTag       string `json:"scene_tag"`
	FunctionTag    string `json:"function_tag"`
}

type BatchAcceptResponse struct {
	AcceptedCount int               `json:"accepted_count"`
	Items         []PhraseBlockView `json:"items"`
}
