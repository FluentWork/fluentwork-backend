package topic

import (
	"fmt"
	"strings"
)

// GeneratePrompt asks the LLM for three workplace speaking prompts.
func GeneratePrompt(sig Signals, forDate string) string {
	scenes := joinCounts(sig.SceneCounts)
	fns := joinCounts(sig.FunctionCounts)
	titles := strings.Join(sig.RecentTitles, "; ")
	if scenes == "" {
		scenes = "(none — use general workplace English)"
	}
	if fns == "" {
		fns = "(none — mix report, clarify, and propose)"
	}
	if titles == "" {
		titles = "(none)"
	}
	level := sig.Level
	if level == "" {
		level = "intermediate"
	}
	return fmt.Sprintf(`You write 3 spoken English practice topic cards for one workplace learner.

Date: %s
Level: %s
Scene tag counts (30d): %s
Function tag counts (30d): %s
Recent session titles (14d): %s

The learner's own phrase blocks (use these, do not invent other subjects):
%s

Reply with JSON only:
{"cards":[{"title":"...","prompt_en":"...","prompt_zh":"...","card_type":"warmup|practice|stretch","seed_tags":["standup","report"]}]}
Exactly 3 cards.

Rules:
- every card must be about the learner's own work, drawn from the block list above; a topic that would suit any learner is a failed card
- seed_tags must be scene or function tags that appear in the counts above, and each card must be one the learner's blocks can serve
- prompt_en is 2-3 opening sentences the learner can say out loud, using their kind of phrasing
- card_type must be one of warmup, practice, stretch
- keep prompts speakable in under 45 seconds`, forDate, level, scenes, fns, titles, blockLines(sig.Blocks))
}

// blockLines renders a bounded slice of the learner's expressions for the
// prompt. Bounded because the request is per user per day: an unbounded corpus
// would make the bill grow with how good a customer they are.
func blockLines(blocks []BlockRef) string {
	if len(blocks) == 0 {
		return "(none)"
	}
	limit := len(blocks)
	if limit > MaxPromptBlocks {
		limit = MaxPromptBlocks
	}
	lines := make([]string, 0, limit)
	for _, block := range blocks[:limit] {
		tag := strings.TrimSpace(block.SceneTag)
		if tag == "" {
			tag = strings.TrimSpace(block.FunctionTag)
		}
		expression := strings.TrimSpace(block.ExpressionEN)
		if expression == "" {
			continue
		}
		if tag != "" {
			lines = append(lines, fmt.Sprintf("- [%s] %s", tag, expression))
			continue
		}
		lines = append(lines, "- "+expression)
	}
	if len(lines) == 0 {
		return "(none)"
	}
	return strings.Join(lines, "\n")
}

func joinCounts(m map[string]int) string {
	if len(m) == 0 {
		return ""
	}
	parts := make([]string, 0, len(m))
	for k, n := range m {
		parts = append(parts, fmt.Sprintf("%s=%d", k, n))
	}
	return strings.Join(parts, ", ")
}
