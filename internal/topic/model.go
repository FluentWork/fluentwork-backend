// Package topic implements B23 daily topic cards, checkin, and streak.
package topic

import "time"

const (
	// CardsPerDay is how many cards the daily generator inserts.
	CardsPerDay = 3
	// MaxReflectionLen is the POST /topic-cards/:id/checkin reflection byte cap.
	MaxReflectionLen = 500
	// GenerateTimeout is the per-attempt LLM budget.
	GenerateTimeout = 15 * time.Second
	// GenerateAttempts is the LLM retry count before skipping a user.
	GenerateAttempts = 3
	// ActiveLookback is the practice-session window for the daily batch.
	ActiveLookback = 30 * 24 * time.Hour
	// MaxInFlight is the daily-batch concurrency cap.
	MaxInFlight = 64
	// CronHourUTC is when the worker may start the daily run.
	CronHourUTC = 4

	// CardTypeWarmup is a lighter opener.
	CardTypeWarmup = "warmup"
	// CardTypePractice is the main workplace prompt.
	CardTypePractice = "practice"
	// CardTypeStretch is a slightly harder stretch prompt.
	CardTypeStretch = "stretch"

	// SkipLLMTimeout is recorded when all LLM attempts fail.
	SkipLLMTimeout = "llm_timeout"
	// SkipParseError is recorded when the LLM payload is not three cards of JSON.
	SkipParseError = "parse_error"
	// SkipAlreadyGenerated is recorded when the user already has cards for the date.
	SkipAlreadyGenerated = "already_generated"
	// SkipNoLLM is recorded when Completer is nil.
	SkipNoLLM = "no_llm"
)

// Card is one generated prompt for a UTC calendar day.
type Card struct {
	ID          string     `json:"id"`
	UserID      string     `json:"-"`
	ForDate     time.Time  `json:"for_date"`
	Title       string     `json:"title"`
	PromptEN    string     `json:"prompt_en"`
	PromptZH    string     `json:"prompt_zh"`
	CardType    string     `json:"card_type"`
	SeedTags    []string   `json:"seed_tags"`
	ValidUntil  time.Time  `json:"valid_until"`
	CheckedInAt *time.Time `json:"checked_in_at,omitempty"`
	DeletedAt   *time.Time `json:"-"`
	CreatedAt   time.Time  `json:"created_at"`
	UpdatedAt   time.Time  `json:"updated_at"`
}

// Checkin is one recorded card completion.
type Checkin struct {
	ID         string
	CardID     string
	UserID     string
	Reflection string
	CreatedAt  time.Time
}

// Streak is the user's checkin streak in UTC days.
type Streak struct {
	UserID          string
	CurrentStreak   int
	LongestStreak   int
	LastCheckinDate *time.Time
	DeletedAt       *time.Time
	UpdatedAt       time.Time
}

// CheckinResult is returned by POST /topic-cards/:id/checkin.
type CheckinResult struct {
	CheckinID  string `json:"checkin_id"`
	StreakDays int    `json:"streak_days"`
}

// CheckinRequest is the optional reflection body.
type CheckinRequest struct {
	Reflection string `json:"reflection"`
}

// ListResponse is GET /topic-cards.
type ListResponse struct {
	Items []Card `json:"items"`
}

// GeneratedCard is one LLM card before persistence.
type GeneratedCard struct {
	Title    string   `json:"title"`
	PromptEN string   `json:"prompt_en"`
	PromptZH string   `json:"prompt_zh"`
	CardType string   `json:"card_type"`
	SeedTags []string `json:"seed_tags"`
}

// GenerateResult is the LLM JSON payload.
type GenerateResult struct {
	Cards []GeneratedCard `json:"cards"`
}

// Signals is the generator input derived from recent practice.
type Signals struct {
	SceneCounts    map[string]int
	FunctionCounts map[string]int
	Level          string
	RecentTitles   []string
	Empty          bool
}
