package topic

import (
	"strconv"
	"strings"
)

// groundCard attaches the learner's own blocks to a card and refuses cards that
// cannot be traced back to their corpus (PRD §7.8 H2: 仅从用户素材主题与语料场景
// 标签派生；每张卡标注来源；禁止泛话题).
//
// The check is **content**, not tags. Tag matching was the first version and it
// let 5 of 9 cards through as generic — a card about system design passed because
// the learner happened to own an interview-tagged block (86_ F3). Requiring the
// card's opening lines to quote one of the learner's own phrases is the rule the
// prompt states and the rule the server verifies.
func groundCard(card Card, sig Signals, recentTitles []string) (Card, bool) {
	text := card.Title + " " + card.PromptEN + " " + card.PromptZH
	matched := contentMatch(text, sig.Blocks)
	if len(matched) == 0 {
		return Card{}, false
	}
	if len(matched) > MaxBlocksPerCard {
		matched = matched[:MaxBlocksPerCard]
	}
	card.BlockIDs = make([]string, 0, len(matched))
	scenes := make([]string, 0, len(matched))
	seenScene := map[string]struct{}{}
	for _, block := range matched {
		card.BlockIDs = append(card.BlockIDs, block.ID)
		scene := strings.ToLower(strings.TrimSpace(block.SceneTag))
		if scene == "" {
			continue
		}
		if _, ok := seenScene[scene]; !ok {
			seenScene[scene] = struct{}{}
			scenes = append(scenes, scene)
		}
	}
	card.SeedTags = scenes
	card.SourceNote = sourceNote(scenes, matched, sig)
	if isRepeatTitle(card.Title, recentTitles) {
		return Card{}, false
	}
	return card, true
}

// minQuoteWords is how many consecutive words a card must share with a block
// before the card counts as derived from it. Four is long enough that ordinary
// workplace phrasing ("I think we should") cannot match by accident, and short
// enough to survive the small edits a model makes when copying.
const minQuoteWords = 4

// contentMatch returns the learner's blocks whose wording appears in the text.
func contentMatch(text string, blocks []BlockRef) []BlockRef {
	words := tokenize(text)
	if len(words) < minQuoteWords {
		return nil
	}
	out := make([]BlockRef, 0, len(blocks))
	for _, block := range blocks {
		if longestSharedRun(words, tokenize(block.ExpressionEN)) >= minQuoteWords {
			out = append(out, block)
			continue
		}
		if longestSharedRun(words, tokenize(block.AnchorUserSaid)) >= minQuoteWords {
			out = append(out, block)
		}
	}
	return out
}

// longestSharedRun returns the longest run of consecutive words the two
// sequences share, order preserved.
func longestSharedRun(a, b []string) int {
	if len(a) == 0 || len(b) == 0 {
		return 0
	}
	prev := make([]int, len(b)+1)
	best := 0
	for i := 1; i <= len(a); i++ {
		cur := make([]int, len(b)+1)
		for j := 1; j <= len(b); j++ {
			if a[i-1] == b[j-1] {
				cur[j] = prev[j-1] + 1
				if cur[j] > best {
					best = cur[j]
				}
			}
		}
		prev = cur
	}
	return best
}

// tokenize lowercases and strips punctuation so quoting survives formatting.
func tokenize(s string) []string {
	fields := strings.FieldsFunc(strings.ToLower(s), func(r rune) bool {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9', r >= 0x4e00 && r <= 0x9fff:
			return false
		default:
			return true
		}
	})
	return fields
}

// isRepeatTitle reports whether the card repeats one the learner already has.
// The generator has no memory of previous days; without this, three days of
// runs produced the same "Daily Standup Progress Update" three times (86_ F3).
func isRepeatTitle(title string, recentTitles []string) bool {
	normalized := strings.Join(tokenize(title), " ")
	if normalized == "" {
		return false
	}
	for _, recent := range recentTitles {
		if strings.Join(tokenize(recent), " ") == normalized {
			return true
		}
	}
	return false
}

// matchBlocks returns the learner's blocks that carry one of the tags, in corpus
// order so the list is stable between runs.
//
// A tag counts as a match when it is equal to a block's scene or function tag,
// or when one contains the other — a model that writes "daily standup" for the
// learner's "standup" is naming the same practice. There is deliberately no
// substitute tag: rewriting a card's tags to whatever the learner happens to do
// most would ship "ordering coffee" with standup blocks attached, which is the
// generic topic H2 forbids, only harder to spot.
// sourceNote is the provenance line the card shows (H2: 每张卡标注来源).
func sourceNote(tags []string, matched []BlockRef, sig Signals) string {
	parts := make([]string, 0, 3)
	if len(tags) > 0 {
		parts = append(parts, "来自你 "+strings.Join(tags, "/")+" 的语料")
	} else {
		parts = append(parts, "来自你的语料库")
	}
	parts = append(parts, "可调用话术块 ×"+strconv.Itoa(len(matched)))
	if len(sig.RecentTitles) > 0 {
		recent := sig.RecentTitles
		if len(recent) > 3 {
			recent = recent[:3]
		}
		parts = append(parts, "最近练过 "+strings.Join(recent, "、"))
	}
	return capRunes(strings.Join(parts, "；"), 255)
}

// capRunes truncates s to at most n runes, never splitting a multi-byte rune.
func capRunes(s string, n int) string {
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
