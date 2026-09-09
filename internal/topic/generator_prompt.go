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

Reply with JSON only:
{"cards":[{"title":"...","prompt_en":"...","prompt_zh":"...","card_type":"warmup|practice|stretch","seed_tags":["standup","report"]}]}
Exactly 3 cards. card_type must be one of warmup, practice, stretch. Keep prompts speakable in under 45 seconds.`, forDate, level, scenes, fns, titles)
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
