package main

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"github.com/FluentWork/fluentwork-backend/internal/orchestrator"
)

// referee is the L2 judge: a model with an explicit rubric, scoring the quality
// of what the chain produced.
//
// It exists because L1 checks can only say "well-formed". Whether a rewrite is
// faithful, whether it sounds like a native speaker, whether a topic card is
// really about this learner — those need a judgement, and a judgement needs a
// rubric or it is just taste.
//
// One call per sample scores everything, so the eval's own cost stays bounded.
type referee struct {
	client orchestrator.Client
}

type refereeInput struct {
	Scene      string
	Transcript string
	Blocks     []refineBlock
	ReviewJSON string
	Topics     []topicOutcome
}

// rubricVerdict is the parsed referee output.
type rubricVerdict struct {
	Blocks []struct {
		Index     int    `json:"index"`
		Fidelity  int    `json:"fidelity"`
		Idiomatic int    `json:"idiomatic"`
		Portable  int    `json:"portable"`
		Comment   string `json:"comment"`
	} `json:"blocks"`
	Review struct {
		Authenticity int    `json:"authenticity"`
		Comment      string `json:"comment"`
	} `json:"review"`
	Topics []struct {
		Index    int    `json:"index"`
		Grounded int    `json:"grounded"`
		Generic  bool   `json:"generic"`
		Comment  string `json:"comment"`
	} `json:"topics"`
}

func (r *referee) score(ctx context.Context, in refereeInput) (*rubricVerdict, error) {
	if r == nil || r.client == nil {
		return nil, fmt.Errorf("referee is not configured")
	}
	resp, err := r.client.Complete(ctx, orchestrator.CompletionRequest{
		SystemPrompt: refereePrompt,
		Prompt:       refereeUserPrompt(in),
		// Room to finish: at 1500 the response was truncated mid-JSON on longer
		// samples, which surfaced as a parse failure rather than as a budget.
		MaxTokens:   3000,
		Temperature: 0,
		// json_object keeps the shape machine-readable; the rubric itself is in
		// the system prompt where a model treats it as instructions.
		ResponseFormat: "json_object",
		Operation:      "eval.moat.rubric",
	})
	if err != nil {
		return nil, err
	}
	return parseRubric(resp.Content)
}

func parseRubric(raw string) (*rubricVerdict, error) {
	trimmed := strings.TrimSpace(raw)
	if i := strings.Index(trimmed, "{"); i > 0 {
		trimmed = trimmed[i:]
	}
	if j := strings.LastIndex(trimmed, "}"); j >= 0 && j < len(trimmed)-1 {
		trimmed = trimmed[:j+1]
	}
	var verdict rubricVerdict
	if err := json.Unmarshal([]byte(trimmed), &verdict); err != nil {
		return nil, fmt.Errorf("parse rubric: %w (raw: %.200s)", err, raw)
	}
	return &verdict, nil
}

const refereePrompt = `You are a severe reviewer for a workplace-English training product used by Chinese software engineers.
You will see one practice session's transcript and everything the product produced from it.

Score each item 1-5 on the rubric below. Be severe: 3 means merely acceptable, 5 means a native speaker would produce exactly that. Do not award 5s to be nice.

Rubric for each phrase block (expression_en):
- fidelity: does the sentence keep the learner's meaning? 5 = same meaning; 3 = meaning shifted; 1 = meaning changed.
- idiomatic: would a native speaker say it? 5 = natural; 3 = grammatical but stiff; 1 = literal Chinese-to-English.
- portable: can the learner reuse this sentence in other similar situations? 5 = clearly reusable; 1 = only fits this exact moment.

Rubric for the review card:
- authenticity: are the issues it lists real problems in this transcript? 5 = all real and specific; 3 = one is filler; 1 = invented problems.

Rubric for each topic card:
- grounded: judge the card's SUBJECT, not its wording. The server already guarantees each card quotes
  the learner's material, so a quoted phrase proves nothing here. 5 = this topic is something this
  learner actually does (their scenes, their work, the things their blocks are about); 3 = the frame is
  a generic template (standup/interview/1:1 opener) that the learner's material was fitted into;
  1 = the learner's material plays no part in the topic.
- generic: true when the topic is a template that would suit any learner — set this even if the card
  quotes the learner's sentence, because a quoted line stapled onto a stock scenario is exactly the
  failure this field exists to catch.

Reply with JSON only, no prose:
{"blocks":[{"index":0,"fidelity":5,"idiomatic":4,"portable":4,"comment":"<=12 words"}],
 "review":{"authenticity":4,"comment":"<=12 words"},
 "topics":[{"index":0,"grounded":5,"generic":false,"comment":"<=12 words"}]}`

func refereeUserPrompt(in refereeInput) string {
	var b strings.Builder
	fmt.Fprintf(&b, "scene: %s\n\n", in.Scene)
	fmt.Fprintf(&b, "transcript (with the rescue ladders the product gave):\n%s\n\n", in.Transcript)

	b.WriteString("phrase blocks produced:\n")
	if len(in.Blocks) == 0 {
		b.WriteString("(none)\n")
	}
	for i, block := range in.Blocks {
		if block.IntentZH == "" && block.AnchorUserSaid == "" {
			// The corpus view for a topic judgement: the learner's sentences, one
			// per line, nothing else.
			fmt.Fprintf(&b, "%d. %s\n", i, block.ExpressionEN)
			continue
		}
		fmt.Fprintf(&b, "%d. intent_zh=%q expression_en=%q anchor_user_said=%q scene=%s function=%s\n",
			i, block.IntentZH, block.ExpressionEN, block.AnchorUserSaid, block.SceneTag, block.FunctionTag)
	}

	fmt.Fprintf(&b, "\nreview card JSON:\n%s\n\n", in.ReviewJSON)

	b.WriteString("topic cards produced:\n")
	if len(in.Topics) == 0 {
		b.WriteString("(none)\n")
	}
	for i, t := range in.Topics {
		fmt.Fprintf(&b, "%d. title=%q prompt_en=%q blocks=%d source_note=%q\n",
			i, t.Title, t.PromptEN, len(t.BlockIDs), t.SourceNote)
	}
	return b.String()
}
