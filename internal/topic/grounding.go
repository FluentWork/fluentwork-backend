package topic

import (
	"strconv"
	"strings"
)

// groundCard attaches the learner's own blocks to a card and refuses cards that
// cannot be traced back to their corpus (PRD §7.8 H2: 仅从用户素材主题与语料场景
// 标签派生；每张卡标注来源；禁止泛话题).
//
// The prompt asks for grounded cards, but a prompt is a request, not a
// guarantee. This check runs on the server, where a generic card is
// indistinguishable from a good one until someone looks at where it came from.
func groundCard(card Card, sig Signals) (Card, bool) {
	tags := normalizeTags(card.SeedTags)
	matched := matchBlocks(sig.Blocks, tags)
	if len(matched) == 0 {
		return Card{}, false
	}
	if len(matched) > MaxBlocksPerCard {
		matched = matched[:MaxBlocksPerCard]
	}
	card.SeedTags = tags
	card.BlockIDs = make([]string, 0, len(matched))
	for _, block := range matched {
		card.BlockIDs = append(card.BlockIDs, block.ID)
	}
	card.SourceNote = sourceNote(tags, matched, sig)
	return card, true
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
func matchBlocks(blocks []BlockRef, tags []string) []BlockRef {
	if len(tags) == 0 {
		return nil
	}
	out := make([]BlockRef, 0, len(blocks))
	for _, block := range blocks {
		if tagMatches(tags, block.SceneTag) || tagMatches(tags, block.FunctionTag) {
			out = append(out, block)
		}
	}
	return out
}

// tagMatches reports whether any of the card's tags names the block's tag.
func tagMatches(tags []string, blockTag string) bool {
	blockTag = strings.ToLower(strings.TrimSpace(blockTag))
	if blockTag == "" {
		return false
	}
	for _, tag := range tags {
		if tag == blockTag || strings.Contains(tag, blockTag) || strings.Contains(blockTag, tag) {
			return true
		}
	}
	return false
}

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

// normalizeTags lowercases and de-duplicates the model's tags, dropping empties.
func normalizeTags(tags []string) []string {
	seen := make(map[string]struct{}, len(tags))
	out := make([]string, 0, len(tags))
	for _, tag := range tags {
		normalized := strings.ToLower(strings.TrimSpace(tag))
		if normalized == "" {
			continue
		}
		if _, ok := seen[normalized]; ok {
			continue
		}
		seen[normalized] = struct{}{}
		out = append(out, normalized)
	}
	return out
}

// capRunes truncates s to at most n runes, never splitting a multi-byte rune.
func capRunes(s string, n int) string {
	if len([]rune(s)) <= n {
		return s
	}
	return string([]rune(s)[:n])
}
