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
	// SkipBelowThreshold is recorded when the user's corpus is too small to
	// ground a card (PRD §7.8 H1: 语料库 ≥ 20 个话术块，服务端可配).
	SkipBelowThreshold = "below_threshold"
	// SkipUngrounded is recorded when every card the model returned failed H2's
	// grounding check. Sending nothing beats sending a generic topic.
	SkipUngrounded = "ungrounded"
	// DefaultMinBlocks is the PRD threshold: 话术库 ≥ 20 个话术块.
	DefaultMinBlocks = 20
	// MaxBlocksPerCard caps the 可调用话术块清单 on one card.
	MaxBlocksPerCard = 5
	// MaxPromptBlocks bounds how many of the user's expressions go into the
	// prompt, so a large corpus cannot inflate the request.
	MaxPromptBlocks = 15
)

// Card is one generated prompt for a UTC calendar day.
type Card struct {
	ID       string    `json:"id"`
	UserID   string    `json:"-"`
	ForDate  time.Time `json:"for_date"`
	Title    string    `json:"title"`
	PromptEN string    `json:"prompt_en"`
	PromptZH string    `json:"prompt_zh"`
	CardType string    `json:"card_type"`
	SeedTags []string  `json:"seed_tags"`
	// BlockIDs is the 可调用话术块清单 (PRD §7.8 H1): the learner's own blocks
	// this topic can put to work.
	BlockIDs []string `json:"block_ids"`
	// SourceNote is H2's provenance line — which of the learner's materials and
	// tags this card came from. A card without one is a card the server could
	// not ground.
	SourceNote  string     `json:"source_note,omitempty"`
	ValidUntil  time.Time  `json:"valid_until"`
	CheckedInAt *time.Time `json:"checked_in_at,omitempty"`
	// DismissedAt / DismissReason record "I did not get to use this, and why"
	// (86_ M11). A dismissal carries no penalty: it is the only signal the
	// product gets about the last mile it cannot otherwise observe.
	DismissedAt   *time.Time `json:"dismissed_at,omitempty"`
	DismissReason string     `json:"dismiss_reason,omitempty"`
	DeletedAt     *time.Time `json:"-"`
	CreatedAt     time.Time  `json:"created_at"`
	UpdatedAt     time.Time  `json:"updated_at"`
}

// Dismissal reasons: a closed set, because the point is a countable signal
// about the last mile, not free text nobody aggregates.
const (
	// DismissNoPartner is "I had nobody to speak English with" — argues for
	// in-app simulated conversation.
	DismissNoPartner = "no_partner"
	// DismissNotConfident is "I did not dare" — argues for a lower first step.
	DismissNotConfident = "not_confident"
	// DismissNoTime is "no time" — argues for shorter cards.
	DismissNoTime = "no_time"
	// DismissNotRelevant is "this topic was not useful" — argues that topic
	// generation is still off, and is a quality signal about H1/H2.
	DismissNotRelevant = "not_relevant"
)

// ValidDismissReason reports whether reason is one of the closed set.
func ValidDismissReason(reason string) bool {
	switch reason {
	case DismissNoPartner, DismissNotConfident, DismissNoTime, DismissNotRelevant:
		return true
	default:
		return false
	}
}

// DismissRequest is the POST /topic-cards/:id/dismiss body.
type DismissRequest struct {
	Reason string `json:"reason"`
}

// DismissResult reports what the dismissal did.
type DismissResult struct {
	CardID           string `json:"card_id"`
	Reason           string `json:"reason"`
	AlreadyDismissed bool   `json:"already_dismissed"`
}

// Checkin is one recorded card completion.
type Checkin struct {
	ID         string
	CardID     string
	UserID     string
	Reflection string
	// DeletedAt follows the module's soft-delete convention so A4's wipe hides
	// the row — the memory store used to leave checkins visible.
	DeletedAt *time.Time
	CreatedAt time.Time
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
	// RecordedUse is how many of the card's blocks were credited as used in a
	// real conversation (86_ M10). Zero means the learner did not tick any, or
	// did not have the ledger wired — never that the checkin failed.
	RecordedUse int `json:"recorded_use"`
	// IgnoredBlockIDs are ids the client sent that this card never offered.
	// Reported rather than silently dropped: a client sending unknown ids has a
	// bug worth seeing.
	IgnoredBlockIDs []string `json:"ignored_block_ids,omitempty"`
}

// CheckinRequest is the checkin body: an optional reflection, and which of the
// card's phrases the learner actually used with a real person.
//
// The used-block list is the product's only first-hand evidence of practice
// turning into speech (§14.3 维度五); everything else is a proxy.
type CheckinRequest struct {
	Reflection   string   `json:"reflection"`
	UsedBlockIDs []string `json:"used_block_ids"`
}

// ListResponse is GET /topic-cards. Items carry the resolved 话术块清单 so the
// client can show which of the learner's own expressions a topic can use.
type ListResponse struct {
	Items []CardView `json:"items"`
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
	// Blocks is the learner's corpus, as much of it as grounding needs: it
	// feeds the H1 threshold, the prompt's expression list, and H2's matching.
	Blocks []BlockRef
}

// BlockRef is one corpus block as topic generation sees it.
type BlockRef struct {
	ID           string
	ExpressionEN string
	IntentZH     string
	SceneTag     string
	FunctionTag  string
	// AnchorUserSaid is what the learner originally said. A card may quote
	// either this or the refined expression; the raw words are often the more
	// natural opening line.
	AnchorUserSaid string
}

// CardView is one card plus the resolved 话术块清单 the client renders.
type CardView struct {
	Card
	Blocks []CardBlockRef `json:"blocks,omitempty"`
}

// CardBlockRef is one block of a card's list, resolved for display.
type CardBlockRef struct {
	ID           string `json:"id"`
	ExpressionEN string `json:"expression_en"`
	IntentZH     string `json:"intent_zh"`
}
