package main

import (
	"context"
	"fmt"
	"sync"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/corpus"
	"github.com/FluentWork/fluentwork-backend/internal/topic"
)

// sceneTopicResult is one scene group's topic stage.
//
// Grouping exists because H1 gates topic cards on the learner's corpus size: run
// per mock session, the generator sees one or two blocks and pads to fill three
// cards, which measures the padding rather than the product. A scene group is
// ~20 sessions' worth of blocks — the shape a real active learner has.
type sceneTopicResult struct {
	Scene      string `json:"scene"`
	CorpusSize int    `json:"corpus_size"`
	// DuplicateExpressions counts blocks whose normalised expression already
	// existed in the group: the cross-session duplication of 86_ M5, measured
	// rather than asserted.
	DuplicateExpressions int            `json:"duplicate_expressions"`
	Cards                []topicOutcome `json:"cards"`
	Verdict              *rubricVerdict `json:"rubric,omitempty"`
	Error                string         `json:"error,omitempty"`
}

// runTopicStage groups samples by scene, builds one corpus per scene, and runs
// the production topic generator over it.
func runTopicStage(ctx context.Context, deps *deps, samples []sample, results []*sampleResult) []sceneTopicResult {
	groups := map[string][]int{}
	order := make([]string, 0, 5)
	for i, s := range samples {
		if _, ok := groups[s.Scene]; !ok {
			order = append(order, s.Scene)
		}
		groups[s.Scene] = append(groups[s.Scene], i)
	}

	out := make([]sceneTopicResult, len(order))
	var wg sync.WaitGroup
	sem := make(chan struct{}, 3)
	for gi, scene := range order {
		wg.Add(1)
		go func(gi int, scene string) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			out[gi] = runSceneTopic(ctx, deps, scene, results, groups[scene])
		}(gi, scene)
	}
	wg.Wait()
	return out
}

func runSceneTopic(ctx context.Context, deps *deps, scene string, results []*sampleResult, idx []int) sceneTopicResult {
	res := sceneTopicResult{Scene: scene}
	userID := "eval-scene-user-" + scene
	sessionID := "eval-scene-session-" + scene

	store := corpus.NewMemoryStore()
	svc := corpus.NewService(store, nil)
	seen := map[string]struct{}{}
	var blocks []corpus.BatchAcceptBlock
	for _, i := range idx {
		for _, expr := range results[i].Corpus.Expressions {
			key := normalizeExpression(expr)
			if _, dup := seen[key]; dup {
				res.DuplicateExpressions++
				continue
			}
			seen[key] = struct{}{}
			blocks = append(blocks, corpus.BatchAcceptBlock{
				IntentZH: "（场景语料）", ExpressionEN: expr, AnchorUserSaid: expr,
				SceneTag: scene, FunctionTag: "report",
			})
		}
	}
	if len(blocks) == 0 {
		res.Error = "no blocks in this scene group"
		return res
	}
	// BatchAccept validates tags; the compact camera above already uses a valid
	// scene tag, and "report" is in the closed function set.
	if _, err := svc.BatchAccept(ctx, userID, corpus.BatchAcceptRequest{
		SourceSessionID: sessionID, Blocks: blocks,
	}); err != nil {
		res.Error = "accept: " + err.Error()
		return res
	}
	res.CorpusSize = len(blocks)

	topicStore := topic.NewMemoryStore()
	gen := topic.NewGenerator(topicStore, &topic.OrchestratorAdapter{Client: deps.client},
		topic.PracticeSignals{Blocks: store})
	// The production threshold (PRD §7.8: 语料库 ≥ 20). A group below it is
	// reported as such rather than silently lowered: the threshold is part of
	// what is under test.
	gen.SetMinBlocks(topic.DefaultMinBlocks)

	day := time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)
	if err := gen.GenerateForUser(ctx, userID, day); err != nil {
		res.Error = "generate: " + err.Error()
		return res
	}
	cards, err := topicStore.ListTodayCards(ctx, userID, day)
	if err != nil {
		res.Error = "list: " + err.Error()
		return res
	}
	for _, card := range cards {
		res.Cards = append(res.Cards, topicOutcome{
			Title: card.Title, CardType: card.CardType, PromptEN: card.PromptEN,
			BlockIDs: card.BlockIDs, SourceNote: card.SourceNote,
		})
	}
	if len(res.Cards) == 0 {
		res.Error = fmt.Sprintf("no cards produced from a %d-block corpus (threshold %d)",
			res.CorpusSize, topic.DefaultMinBlocks)
		return res
	}

	verdict, err := deps.referee.score(ctx, refereeInput{
		Scene: scene, Transcript: "(scene group: " + scene + ")",
		Topics: res.Cards,
	})
	if err != nil {
		res.Error = "referee: " + err.Error()
		return res
	}
	res.Verdict = verdict
	return res
}
