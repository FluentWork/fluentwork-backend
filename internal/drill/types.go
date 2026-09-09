// Package drill implements E1/E2 flash-drill rounds, LLM judging, and SM-2 updates.
package drill

import "time"

const (
	// DefaultRoundSize is the number of cards GET /drill/round returns.
	DefaultRoundSize = 10
	// MaxRoundSize caps size query param.
	MaxRoundSize = 20
	// JudgeTimeout is the D-3 budget for one semantic judge call.
	JudgeTimeout = 1500 * time.Millisecond
	// DrillTypeRecall is E1 层级1召回闪测.
	DrillTypeRecall = 1
)

// Card is one due phrase block in a drill round.
type Card struct {
	BlockID      string `json:"block_id"`
	IntentZH     string `json:"intent_zh"`
	ExpressionEN string `json:"expression_en"`
	State        string `json:"state"`
}

// Round is the GET /drill/round payload.
type Round struct {
	Size  int    `json:"size"`
	Cards []Card `json:"cards"`
}

// JudgeRequest is POST /drill/judge.
type JudgeRequest struct {
	BlockID    string `json:"block_id"`
	ASRText    string `json:"asr_text"`
	ResponseMS int    `json:"response_ms"`
	SessionID  string `json:"session_id"`
}

// JudgeResponse is returned after semantic judge + SM-2 update.
type JudgeResponse struct {
	Pass          bool   `json:"pass"`
	JudgeReason   string `json:"judge_reason"`
	SuccessStreak int    `json:"success_streak"`
	State         string `json:"state"`
	NextDueAt     string `json:"next_due_at"`
	Recorded      bool   `json:"recorded"`
}

// Record is one drill_records row.
type Record struct {
	ID           int64
	UserID       string
	BlockID      string
	SessionID    string
	DrillType    int
	SemanticPass bool
	ResponseMS   int
	ASRText      string
	JudgeReason  string
	CreatedAt    time.Time
}

// JudgeResult is the parsed LLM JSON.
type JudgeResult struct {
	Pass   bool   `json:"pass"`
	Reason string `json:"judge_reason"`
}
