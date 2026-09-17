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
			{ID: "b1", ExpressionEN: "The deploy is blocked on the migration.", AnchorUserSaid: "the deploy is waiting", SceneTag: "standup", FunctionTag: "report"},
			{ID: "b2", ExpressionEN: "I'll touch base with the team tomorrow.", AnchorUserSaid: "sync up with the team", SceneTag: "standup", FunctionTag: "commit"},
			{ID: "b3", ExpressionEN: "Can we park that for now?", AnchorUserSaid: "let us talk later", SceneTag: "review", FunctionTag: "defer"},
		},
	}
}

// A card that quotes the learner's own phrase is grounded, and the block it
// quoted is the one attached — content matching, not a tag guess.
func TestGroundCard_QuotedPhraseGroundsTheCard(t *testing.T) {
	card := Card{
		Title:    "Standup: deploy status",
		PromptEN: "Hey team, the deploy is blocked on the migration, so I need another day.",
	}
	grounded, ok := groundCard(card, testSignals(), nil)
	if !ok {
		t.Fatal("a card quoting the learner's phrase must ground")
	}
	if len(grounded.BlockIDs) != 1 || grounded.BlockIDs[0] != "b1" {
		t.Fatalf("block list = %v, want the quoted block", grounded.BlockIDs)
	}
	if grounded.SourceNote == "" {
		t.Fatal("a grounded card carries its provenance")
	}
}

// Quoting the learner's original words counts too: it is still their material.
func TestGroundCard_QuotingTheAnchorCounts(t *testing.T) {
	card := Card{Title: "Sync", PromptEN: "Quick one: sync up with the team — when works for you?"}
	grounded, ok := groundCard(card, testSignals(), nil)
	if !ok || len(grounded.BlockIDs) != 1 || grounded.BlockIDs[0] != "b2" {
		t.Fatalf("anchor quote must ground the card: %+v ok=%v", grounded.BlockIDs, ok)
	}
}

// The tag-level check this replaces let a generic card through whenever the
// learner owned a block with a matching tag (86_ F3: 5 of 9 cards).
func TestGroundCard_GenericCardIsRefusedEvenWithMatchingTags(t *testing.T) {
	card := Card{
		Title:    "Technical Interview: Rate Limiter Design",
		PromptEN: "Thanks for having me. I would use a token bucket stored in Redis.",
		SeedTags: []string{"interview", "report"},
	}
	if _, ok := groundCard(card, testSignals(), nil); ok {
		t.Fatal("a card sharing no wording with the corpus must be refused")
	}
}

func TestGroundCard_RefusesRepeatedTopics(t *testing.T) {
	card := Card{Title: "Daily Standup Progress Update", PromptEN: "Hey team, the deploy is blocked on the migration."}
	if _, ok := groundCard(card, testSignals(), []string{"Daily Standup Progress Update"}); ok {
		t.Fatal("a card repeating a recent title must be refused")
	}
	// The same title with different punctuation/case is still a repeat.
	if _, ok := groundCard(card, testSignals(), []string{"daily standup progress update!"}); ok {
		t.Fatal("title comparison must ignore punctuation and case")
	}
	if _, ok := groundCard(card, testSignals(), []string{"A different topic"}); !ok {
		t.Fatal("an unrelated recent title must not block the card")
	}
}

func TestGroundCard_CapsTheBlockList(t *testing.T) {
	sig := Signals{}
	for i := 0; i < MaxBlocksPerCard+3; i++ {
		sig.Blocks = append(sig.Blocks, BlockRef{
			ID:           "b" + string(rune('a'+i)),
			ExpressionEN: "the deploy is blocked on the migration",
			SceneTag:     "standup",
		})
	}
	sig.SceneCounts = map[string]int{"standup": len(sig.Blocks)}
	card, ok := groundCard(Card{Title: "Status", PromptEN: "the deploy is blocked on the migration"}, sig, nil)
	if !ok {
		t.Fatal("grounding failed")
	}
	if len(card.BlockIDs) != MaxBlocksPerCard {
		t.Fatalf("block list = %d, want the cap %d", len(card.BlockIDs), MaxBlocksPerCard)
	}
}

func TestLongestSharedRun(t *testing.T) {
	cases := []struct {
		a, b []string
		want int
	}{
		{[]string{"the", "deploy", "is", "blocked"}, []string{"the", "deploy", "is", "blocked"}, 4},
		{[]string{"hey", "the", "deploy", "is", "blocked", "today"}, []string{"the", "deploy", "is", "blocked"}, 4},
		{[]string{"the", "deploy", "is", "fine"}, []string{"the", "deploy", "is", "blocked"}, 3},
		{[]string{"a", "b"}, []string{"c", "d"}, 0},
		{nil, []string{"a"}, 0},
	}
	for _, tc := range cases {
		if got := longestSharedRun(tc.a, tc.b); got != tc.want {
			t.Errorf("longestSharedRun(%v, %v) = %d, want %d", tc.a, tc.b, got, tc.want)
		}
	}
}

// H2's other half: when nothing grounds, the user is skipped rather than served
// a plausible-looking generic topic.
func TestGenerate_AllCardsUngroundedSkipsUser(t *testing.T) {
	blocks := make([]BlockRef, 0, DefaultMinBlocks)
	for i := 0; i < DefaultMinBlocks; i++ {
		blocks = append(blocks, BlockRef{ID: "b-" + itoa(i), ExpressionEN: "the deploy is blocked on the migration", SceneTag: "standup"})
	}
	sig := stubSignals{sig: Signals{Level: "intermediate", Blocks: blocks}}
	llm := &stubLLM{body: `{"cards":[
		{"title":"Ordering coffee","prompt_en":"Order a flat white please.","prompt_zh":"点咖啡","card_type":"warmup","seed_tags":["travel"]},
		{"title":"Party small talk","prompt_en":"Ask about hobbies today.","prompt_zh":"聊爱好","card_type":"practice","seed_tags":["social"]},
		{"title":"Weekend","prompt_en":"Describe your weekend plans.","prompt_zh":"周末","card_type":"stretch","seed_tags":["casual"]}
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

// The prompt carries the rules the server enforces: quote the learner verbatim,
// and do not repeat what they already have.
func TestGeneratePrompt_StatesQuoteAndRepeatRules(t *testing.T) {
	blocks := make([]BlockRef, 0, MaxPromptBlocks+10)
	for i := 0; i < MaxPromptBlocks+10; i++ {
		blocks = append(blocks, BlockRef{ID: "b", ExpressionEN: "expression " + itoa(i), SceneTag: "standup"})
	}
	prompt := GeneratePrompt(Signals{Blocks: blocks}, "2026-09-18", []string{"Daily Standup Progress Update"})
	for _, want := range []string{
		"learner's own phrase blocks",
		"verbatim",
		"discarded by the server",
		"do not repeat any topic or title",
		"Daily Standup Progress Update",
	} {
		if !strings.Contains(prompt, want) {
			t.Fatalf("prompt missing %q", want)
		}
	}
	if strings.Contains(prompt, "expression "+itoa(MaxPromptBlocks)) {
		t.Fatal("prompt block list must be bounded")
	}
}
