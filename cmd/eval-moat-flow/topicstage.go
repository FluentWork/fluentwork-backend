package main

import (
	"context"
	"fmt"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/corpus"
	"github.com/FluentWork/fluentwork-backend/internal/topic"
)

// topicStageResult is the topic-card half of the run.
//
// The corpus is pooled across every mock session on purpose. H1 gates topic
// cards on a learner's corpus size (≥20 blocks), and one scene group's worth of
// mock sessions yields 15–19 unique blocks — just under the gate, so the stage
// would report "nothing produced" for reasons that say more about the mock than
// about the product. Pooling all sessions is also the more faithful shape: a
// real learner's corpus mixes scenes, and that is what the generator reads.
type topicStageResult struct {
	CorpusSize int            `json:"corpus_size"`
	Days       int            `json:"days"`
	Cards      []topicOutcome `json:"cards"`
	Verdict    *rubricVerdict `json:"rubric,omitempty"`
	Error      string         `json:"error,omitempty"`
	// SceneDuplicates measures 86_ M5 per scene: how many refined expressions a
	// group of sessions produced twice. It needs no model call, so it is
	// measured for every scene regardless of the corpus gate.
	SceneDuplicates []sceneDuplicate `json:"scene_duplicates"`
}

type sceneDuplicate struct {
	Scene      string `json:"scene"`
	Blocks     int    `json:"blocks"`
	Duplicates int    `json:"duplicates"`
	Sessions   int    `json:"sessions"`
}

// topicDays is how many consecutive days of cards the stage generates: three
// cards a day is the product's rate, and one day's worth is too small a sample
// for a genericness rate.
const topicDays = 3

func runTopicStage(ctx context.Context, deps *deps, samples []sample, results []*sampleResult) topicStageResult {
	out := topicStageResult{Days: topicDays}

	// Per-scene duplication first: it is free and independent of the gate.
	byScene := map[string]*sceneDuplicate{}
	bySceneSeen := map[string]map[string]struct{}{}
	order := make([]string, 0, 5)
	for i, s := range samples {
		stat, ok := byScene[s.Scene]
		if !ok {
			stat = &sceneDuplicate{Scene: s.Scene}
			byScene[s.Scene] = stat
			bySceneSeen[s.Scene] = map[string]struct{}{}
			order = append(order, s.Scene)
		}
		stat.Sessions++
		// Duplication is measured across the sessions of a scene, so the set
		// lives on the scene — not on the sample, which would only ever find
		// duplicates inside one session.
		seen := bySceneSeen[s.Scene]
		for _, expr := range results[i].Corpus.Expressions {
			key := normalizeExpression(expr)
			if _, dup := seen[key]; dup {
				stat.Duplicates++
			}
			seen[key] = struct{}{}
			stat.Blocks++
		}
	}
	for _, scene := range order {
		out.SceneDuplicates = append(out.SceneDuplicates, *byScene[scene])
	}

	// Now the pooled corpus.
	userID := "eval-topic-user"
	sessionID := "eval-topic-session"
	store := corpus.NewMemoryStore()
	svc := corpus.NewService(store, nil)

	seen := map[string]struct{}{}
	var blocks []corpus.BatchAcceptBlock
	for _, r := range results {
		for _, expr := range r.Corpus.Expressions {
			key := normalizeExpression(expr)
			if _, dup := seen[key]; dup {
				continue
			}
			seen[key] = struct{}{}
			// The scene tag must come from the closed enum; the block's own tag
			// is not carried on the accepted view, so the sample's scene is used
			// and the function tag stays in the enum.
			blocks = append(blocks, corpus.BatchAcceptBlock{
				IntentZH: "（评估语料）", ExpressionEN: expr, AnchorUserSaid: expr,
				SceneTag: r.Sample.Scene, FunctionTag: "report",
			})
		}
	}
	if len(blocks) == 0 {
		out.Error = "no blocks were produced by any sample"
		return out
	}
	accept, err := svc.BatchAccept(ctx, userID, corpus.BatchAcceptRequest{
		SourceSessionID: sessionID, Blocks: blocks,
	})
	if err != nil {
		out.Error = "accept: " + err.Error()
		return out
	}
	// The corpus is smaller than the input when the accept merged repeats
	// (86_ M5); the size reported is the corpus the generator actually saw.
	out.CorpusSize = len(accept.Items)
	blockByID := make(map[string]refineBlock, len(accept.Items))
	for _, item := range accept.Items {
		blockByID[item.ID] = refineBlock{
			IntentZH:       item.IntentZH,
			ExpressionEN:   item.ExpressionEN,
			AnchorUserSaid: item.AnchorUserSaid,
			SceneTag:       item.SceneTag,
			FunctionTag:    item.FunctionTag,
		}
	}

	topicStore := topic.NewMemoryStore()
	gen := topic.NewGenerator(topicStore, &topic.OrchestratorAdapter{Client: deps.client},
		topic.PracticeSignals{Blocks: store})
	// The production threshold (PRD §7.8). A corpus below it is reported as such:
	// the gate is part of what is under test.
	gen.SetMinBlocks(topic.DefaultMinBlocks)

	baseDay := time.Date(2026, 9, 18, 0, 0, 0, 0, time.UTC)
	for day := 0; day < topicDays; day++ {
		target := baseDay.AddDate(0, 0, day)
		if err := gen.GenerateForUser(ctx, userID, target); err != nil {
			out.Error = fmt.Sprintf("generate day %d: %v", day, err)
			return out
		}
		cards, err := topicStore.ListTodayCards(ctx, userID, target)
		if err != nil {
			out.Error = fmt.Sprintf("list day %d: %v", day, err)
			return out
		}
		for _, card := range cards {
			out.Cards = append(out.Cards, topicOutcome{
				Title: card.Title, CardType: card.CardType, PromptEN: card.PromptEN,
				BlockIDs: card.BlockIDs, SourceNote: card.SourceNote,
			})
		}
	}
	if len(out.Cards) == 0 {
		out.Error = fmt.Sprintf("no cards produced from a %d-block corpus (threshold %d)",
			out.CorpusSize, topic.DefaultMinBlocks)
		return out
	}

	// The referee judges each card against **the blocks that card claims**, not
	// against a corpus sample: H2 asks "is this topic derived from the learner's
	// material", and the only material a card claims is its block list. Judging
	// against a sample of the corpus measured the sample — the referee kept
	// answering "not from learner blocks" for blocks it simply had not been shown.
	// The referee reads the learner's whole corpus — expressions only, which keeps
	// the prompt small — because the question is whether the card's *subject*
	// comes from this learner or is a template their material was fitted into.
	// Judging against the card's own claimed blocks made the answer near-tautological
	// (the server guarantees the quote); judging against a sample measured the
	// sample. The whole corpus is the only view that can tell the two apart.
	claimed := make([]refineBlock, 0, len(accept.Items))
	for _, item := range accept.Items {
		claimed = append(claimed, refineBlock{
			IntentZH:       "",
			ExpressionEN:   item.ExpressionEN,
			AnchorUserSaid: "",
			SceneTag:       "standup",
			FunctionTag:    "report",
		})
	}

	verdict, err := deps.referee.score(ctx, refereeInput{
		Scene:      "(pooled across scenes)",
		Transcript: "(evaluation corpus: this learner's own phrase blocks are listed below)",
		Blocks:     claimed,
		Topics:     out.Cards,
	})
	if err != nil {
		out.Error = "referee: " + err.Error()
		return out
	}
	out.Verdict = verdict
	return out
}
