// Package drill implements E1/E2 flash-drill rounds, LLM judging, and SM-2 updates.
package drill

import (
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/corpus"
)

const (
	// DefaultRoundSize is the number of cards GET /drill/round returns.
	DefaultRoundSize = 10
	// MaxRoundSize caps size query param.
	MaxRoundSize = 20
	// JudgeTimeout is the budget for one semantic judge call.
	//
	// It was 1.5s by design (D-3 assumed a realtime pause); measured against the
	// deployed judge endpoint it timed out on 16/16 calls, with real latencies of
	// 1.56s–4.8s (median 1.96s) — so the old budget guaranteed a timeout and the
	// timeout was recorded as a failed answer (86_ F1). 6s covers the measured
	// maximum with headroom; override per deployment with DRILL_JUDGE_TIMEOUT.
	JudgeTimeout = 6 * time.Second
	// DrillTypeRecall is E1 层级1召回闪测.
	DrillTypeRecall = 1
)

// Config carries the E3 server-side knobs (PRD §5.3.2). The ladder itself lives
// in corpus.Schedule because the B7 hit writeback shares it; wiring builds one
// Schedule and hands it to both the corpus store and this service.
type Config struct {
	Schedule corpus.Schedule
	// RoundSize is the cards per round when the client sends no size (E1: 10).
	RoundSize int
	// DailyNewBlockLimit caps how many 灰 blocks may enter rounds per UTC day.
	// Zero means no cap. Training and automated material is never capped: the
	// limit defers new blocks, it does not close the drill.
	DailyNewBlockLimit int
	// OverdueWindow is how far overdue a block may be before a round folds it
	// back to "due now" (83_ §2.1 风险 2: 过期任务不累积). Zero disables the
	// sweep — a zero Config is "no sweep", while DefaultConfig carries the PRD's
	// three days.
	OverdueWindow time.Duration
}

// DefaultConfig is the PRD ladder with the pre-E3 round behaviour.
func DefaultConfig() Config {
	return Config{
		Schedule:           corpus.DefaultSchedule(),
		RoundSize:          DefaultRoundSize,
		DailyNewBlockLimit: 0,
		OverdueWindow:      DefaultOverdueWindow,
	}
}

// DefaultOverdueWindow is 83_ §2.1's "只保留最近 3 天".
const DefaultOverdueWindow = 72 * time.Hour

// Normalize fills anything left unset, so a partially built Config cannot
// silently produce a zero-length round or an unscheduled ladder.
//
// OverdueWindow is deliberately left alone: zero is a meaningful value there
// (the sweep is off), so filling it would make "off" impossible to express.
func (c Config) Normalize() Config {
	out := c
	out.Schedule = out.Schedule.Normalize()
	if out.RoundSize <= 0 {
		out.RoundSize = DefaultRoundSize
	}
	if out.DailyNewBlockLimit < 0 {
		out.DailyNewBlockLimit = 0
	}
	if out.OverdueWindow < 0 {
		out.OverdueWindow = 0
	}
	return out
}

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
	Pass        bool   `json:"pass"`
	JudgeReason string `json:"judge_reason"`
	// Judged is false when the judge could not run (timeout, parse failure,
	// judge unavailable). The schedule is then untouched and Retryable is set:
	// a judge that did not run is not evidence that the learner was wrong.
	Judged bool `json:"judged"`
	// Retryable tells the client this attempt can simply be sent again.
	Retryable     bool   `json:"retryable,omitempty"`
	SuccessStreak int    `json:"success_streak"`
	State         string `json:"state"`
	NextDueAt     string `json:"next_due_at"`
	Recorded      bool   `json:"recorded"`
	// RecordID names this attempt so an appeal can point at it (E2). Zero when
	// the ledger write failed after the schedule had already moved.
	RecordID int64 `json:"record_id,omitempty"`
	// Promoted marks the attempt that turned this block green (E4's 已自动化
	// 变化). The transition is already in State; this says it happened *now*,
	// which is what a celebration needs.
	Promoted bool `json:"promoted,omitempty"`
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
	// Judged is false for attempts the judge could not score (migration 0023).
	Judged      bool
	ResponseMS  int
	ASRText     string
	JudgeReason string
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
	// Judged is false when no verdict was obtained (the sentinel reasons above).
	// Callers must not treat an unjudged result as a failure (86_ F1).
	Judged bool `json:"-"`
	// OmittedDetails flags answers that kept the point but dropped details —
	// passed, yet a signal that the block may be too long to recall whole.
	OmittedDetails bool `json:"omitted_details,omitempty"`
}
