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
	// RecordID names this attempt so an appeal can point at it (E2). Zero when
	// the ledger write failed after the schedule had already moved.
	RecordID int64 `json:"record_id,omitempty"`
	// ASRText echoes what the judge actually read. The client shows it next to
	// the verdict (E2: 判定前展示 ASR 识别文本) so a user who was misheard can
	// see that for themselves instead of guessing.
	ASRText string `json:"asr_text,omitempty"`
}

// AppealRequest is POST /drill/appeal: one tap of "我说的是对的".
type AppealRequest struct {
	RecordID int64 `json:"record_id"`
}

// AppealResponse reports what the appeal did to the block's schedule.
type AppealResponse struct {
	RecordID int64  `json:"record_id"`
	BlockID  string `json:"block_id"`
	// Restored is true when the pre-attempt schedule was put back.
	Restored bool `json:"restored"`
	// AlreadyAppealed is true when this attempt had been appealed before; the
	// call then changes nothing and answers with the block's current state.
	AlreadyAppealed bool   `json:"already_appealed"`
	State           string `json:"state"`
	SuccessStreak   int    `json:"success_streak"`
	NextDueAt       string `json:"next_due_at"`
	// Note explains a non-restoring outcome in one line for the client log.
	Note string `json:"note,omitempty"`
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
	// Prev* snapshots the block's schedule as it stood before this attempt, so
	// an appeal can restore it without guessing (PRD §7.5: 状态不回退、
	// next_due_at 保持原到期时间). Empty PrevState means the snapshot is
	// missing — a row written before the appeal feature — and nothing is
	// restored.
	PrevState         string
	PrevSuccessStreak int
	PrevNextDueAt     time.Time
	// AppealedAt is set once, on the first appeal of this attempt.
	AppealedAt *time.Time
	CreatedAt  time.Time
}

// JudgeResult is the parsed LLM JSON.
type JudgeResult struct {
	Pass   bool   `json:"pass"`
	Reason string `json:"judge_reason"`
}
