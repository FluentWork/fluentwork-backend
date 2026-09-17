package topic

import (
	"context"
	"strings"
	"testing"
	"time"
)

func testSignals() Signals {
	return Signals{
		SceneCounts:    map[string]int{"standup": 4, "review": 2},
		FunctionCounts: map[string]int{"report": 5, "propose": 1},
		Blocks: []BlockRef{
			{ID: "b1", ExpressionEN: "The deploy is blocked on the migration.", SceneTag: "standup", FunctionTag: "report"},
			{ID: "b2", ExpressionEN: "I'll touch base with the team.", SceneTag: "standup", FunctionTag: "commit"},
			{ID: "b3", ExpressionEN: "Can we park that for now?", SceneTag: "review", FunctionTag: "defer"},
		},
	}
}

func TestGroundCard_MatchesSceneAndFunction(t *testing.T) {
	card, ok := groundCard(Card{Title: "Standup", SeedTags: []string{"Standup", " "}}, testSignals())
	if !ok {
		t.Fatal("a card with the learner's own scene must ground")
	}
	if len(card.BlockIDs) != 2 || card.BlockIDs[0] != "b1" || card.BlockIDs[1] != "b2" {
		t.Fatalf("block list = %v", card.BlockIDs)
	}
	if card.SeedTags[0] != "standup" {
		t.Fatalf("tags must be normalized: %v", card.SeedTags)
	}
	if card.SourceNote == "" || !strings.Contains(card.SourceNote, "standup") {
		t.Fatalf("source note = %q", card.SourceNote)
	}
}

// A synonym is a match: "daily standup" names the learner's own "standup".
func TestGroundCard_MatchesTagSynonyms(t *testing.T) {
	card, ok := groundCard(Card{Title: "Daily sync", SeedTags: []string{"daily standup"}}, testSignals())
	if !ok {
		t.Fatal("a containing tag must ground the card")
	}
	if len(card.BlockIDs) == 0 {
		t.Fatal("synonym match must attach blocks")
	}
}

// But an unrelated tag is not: there is no substitution to the learner's
// dominant scene, because that would attach standup blocks to a coffee topic.
func TestGroundCard_NoSubstituteTag(t *testing.T) {
	if _, ok := groundCard(Card{Title: "Ordering coffee", SeedTags: []string{"travel"}}, testSignals()); ok {
		t.Fatal("an unrelated tag must not be rewritten into a grounding")
	}
}

func TestGroundCard_RejectsUngroundedCard(t *testing.T) {
	sig := Signals{
		SceneCounts: map[string]int{"standup": 3},
		Blocks:      []BlockRef{{ID: "b1", ExpressionEN: "x", SceneTag: "standup", FunctionTag: "report"}},
	}
	if _, ok := groundCard(Card{Title: "Ordering coffee", SeedTags: []string{"travel", "social"}}, sig); ok {
		t.Fatal("a card nothing in the corpus can serve must be refused")
	}
	if _, ok := groundCard(Card{Title: "No corpus"}, Signals{}); ok {
		t.Fatal("no blocks at all means nothing to ground on")
	}
}

func TestGroundCard_CapsTheBlockList(t *testing.T) {
	sig := Signals{Blocks: nil}
	for i := 0; i < MaxBlocksPerCard+3; i++ {
		sig.Blocks = append(sig.Blocks, BlockRef{ID: "b" + string(rune('a'+i)), SceneTag: "standup", ExpressionEN: "x"})
	}
	sig.SceneCounts = map[string]int{"standup": len(sig.Blocks)}
	card, ok := groundCard(Card{SeedTags: []string{"standup"}}, sig)
	if !ok {
		t.Fatal("grounding failed")
	}
	if len(card.BlockIDs) != MaxBlocksPerCard {
		t.Fatalf("block list = %d, want the cap %d", len(card.BlockIDs), MaxBlocksPerCard)
	}
}

// H2's other half: when nothing grounds, the user is skipped rather than served
// a plausible-looking generic topic.
func TestGenerate_AllCardsUngroundedSkipsUser(t *testing.T) {
	blocks := make([]BlockRef, 0, DefaultMinBlocks)
	for i := 0; i < DefaultMinBlocks; i++ {
		blocks = append(blocks, BlockRef{ID: "b-" + itoa(i), ExpressionEN: "x", SceneTag: "standup", FunctionTag: "report"})
	}
	// No scene counts and no matching tags: even the fallback has nothing to
	// match, which is the "user has blocks but none for these topics" case.
	sig := stubSignals{sig: Signals{Level: "intermediate", Blocks: blocks}}
	llm := &stubLLM{body: `{"cards":[
		{"title":"Ordering coffee","prompt_en":"Order a flat white.","prompt_zh":"点咖啡","card_type":"warmup","seed_tags":["travel"]},
		{"title":"Party small talk","prompt_en":"Ask about hobbies.","prompt_zh":"聊爱好","card_type":"practice","seed_tags":["social"]},
		{"title":"Weekend","prompt_en":"Describe your weekend.","prompt_zh":"周末","card_type":"stretch","seed_tags":["casual"]}
	]}`}
	_, gen, store := testService(t, llm, sig)
	day := time.Date(2026, 9, 9, 0, 0, 0, 0, time.UTC)
	if err := gen.GenerateForUser(context.Background(), "u1", day); err != nil {
		t.Fatal(err)
	}
	if got, _ := store.ListTodayCards(context.Background(), "u1", day); len(got) != 0 {
		t.Fatalf("generic topics were shipped: %+v", got)
	}
	if !strings.Contains(PrometheusMetrics(), "ungrounded") {
		t.Fatalf("drop not counted: %s", PrometheusMetrics())
	}
}

// The prompt carries the learner's own expressions, bounded: the request is per
// user per day and must not grow with corpus size.
func TestGeneratePrompt_IncludesBoundedBlockList(t *testing.T) {
	blocks := make([]BlockRef, 0, MaxPromptBlocks+10)
	for i := 0; i < MaxPromptBlocks+10; i++ {
		blocks = append(blocks, BlockRef{ID: "b", ExpressionEN: "expression " + itoa(i), SceneTag: "standup"})
	}
	prompt := GeneratePrompt(Signals{Blocks: blocks}, "2026-09-18")
	if !strings.Contains(prompt, "- [standup] expression 0") {
		t.Fatalf("prompt must list the learner's expressions:\n%s", prompt)
	}
	if strings.Contains(prompt, "expression "+itoa(MaxPromptBlocks)) {
		t.Fatal("prompt block list must be bounded")
	}
	// The grounding rule travels with the request, so a compliant model can
	// avoid the drop entirely.
	for _, want := range []string{"learner's own phrase blocks", "failed card", "seed_tags must be"} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q", want)
		}
	}
}
