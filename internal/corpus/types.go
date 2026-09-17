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
	ID             string
	UserID         string
	IntentZH       string
	ExpressionEN   string
	AnchorUserSaid string
	SceneTag       string
	FunctionTag    string
	State          string
	SuccessStreak  int
	NextDueAt      time.Time
	// EaseFactor is 预留，不参与计算。表列与字段都建好了，但 MVP 的调度是
	// 固定阶梯（corpus.ApplyJudge：24h / 7d / 30d 三档 + 失败 1h），没有任何
	// 实现读它——它恒为建块时的默认值 2.5。保留是为了 V1.1 可能启用的简化
	// SM-2（见 §33_/§39_，已在 PRD §5.5 定案为 "MVP 不启用"）。
	// 谁要启用它，必须同时改 corpus.ApplyJudge 并补测试；只改这里等于没改。
	EaseFactor      float64
	RealUseCount    int
	TotalUses       int
	LastUsedAt      *time.Time
	IsFavorite      bool
	PinnedAt        *time.Time
	SourceSessionID *string
	DeletedAt       *time.Time
	CreatedAt       time.Time
	UpdatedAt       time.Time
}

// Feedback reasons (83_ §2.2). A closed set: the point of the reflux is a
// countable signal for prompt work, which free text would not give.
const (
	// FeedbackNotIdiomatic is "地道版还不够地道".
	FeedbackNotIdiomatic = "not_idiomatic"
	// FeedbackNotUseful is "这句话我用不上".
	FeedbackNotUseful = "not_useful"
	// FeedbackWrongMeaning is "改写改变了我的原意".
	FeedbackWrongMeaning = "wrong_meaning"
)

// ValidFeedbackReason reports whether reason is one of the closed set.
func ValidFeedbackReason(reason string) bool {
	switch reason {
	case FeedbackNotIdiomatic, FeedbackNotUseful, FeedbackWrongMeaning:
		return true
	default:
		return false
	}
}

// Feedback is one stored quality signal.
type Feedback struct {
	ID      string
	UserID  string
	BlockID string
	Reason  string
	// DeletedAt follows the corpus soft-delete convention so A4's wipe and
	// restore cover feedback with the blocks it belongs to.
	DeletedAt *time.Time
	CreatedAt time.Time
	// AlreadyRecorded is set by the service when this (user, block, reason) had
	// been reported before; the call is idempotent.
	AlreadyRecorded bool
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
	PinnedOnly   bool
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
