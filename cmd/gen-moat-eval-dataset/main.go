// Command gen-moat-eval-dataset writes the 100-sample mock corpus used by
// cmd/eval-moat-flow.
//
// The dataset is generated rather than hand-written so it stays reproducible and
// reviewable: 20 topic frames (5 scenes × 4 topics each) crossed with 5 speaking
// profiles gives 100 sessions, which is the variety the flow needs to say
// anything about quality across scenes.
//
// Each sample carries machine-checkable expectations, because a quality number
// nobody can verify is just an opinion: the topic keywords the refined
// expression should touch, and two judge cases (an equivalent paraphrase and an
// off-topic answer) that the flash-drill judge must classify correctly.
//
//	go run ./cmd/gen-moat-eval-dataset            # writes eval/moat/dataset.json
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// frame is one scene-specific exchange before any speaking defect is applied.
type frame struct {
	scene string
	topic string
	ai    string
	// answer is correct but plain English — the raw material refine exists to
	// improve.
	answer string
	// topics are keywords the refined expression is expected to touch.
	topics []string
	// paraphrase is semantically equivalent to the answer: the drill judge must
	// pass it.
	paraphrase string
	// offTopic says something unrelated: the judge must fail it.
	offTopic string
}

func frames() []frame {
	return []frame{
		// --- standup ---
		{
			"standup", "deploy_migration",
			"How is the release looking?",
			"The deploy is waiting because the database migration is not finished.",
			[]string{"deploy", "migration"},
			"The rollout is on hold until the database migration lands.",
			"I would like to order a coffee before the meeting.",
		},
		{
			"standup", "api_review",
			"Any blockers today?",
			"I am blocked on the API review because nobody has looked at my pull request.",
			[]string{"api", "review"},
			"I am stuck waiting for someone to review the API pull request.",
			"Yesterday I went hiking with my family.",
		},
		{
			"standup", "oncall_handover",
			"What did you do yesterday?",
			"I finished the on-call handover and wrote down the incidents from last week.",
			[]string{"on-call", "handover"},
			"I wrapped up the on-call handover and documented last week's incidents.",
			"The weather in Beijing is getting colder.",
		},
		{
			"standup", "test_flakiness",
			"Anything to flag?",
			"The integration tests are failing sometimes and I do not know the reason yet.",
			[]string{"test", "flaky"},
			"The integration tests are flaky and I have not found the cause yet.",
			"I am learning to play the guitar on weekends.",
		},

		// --- review (code review discussion) ---
		{
			"review", "naming",
			"What do you think of this function name?",
			"I think the name is not clear and we should choose a longer name for it.",
			[]string{"name", "clear"},
			"The name is unclear, so I would pick something more descriptive.",
			"Let us book the meeting room for Friday.",
		},
		{
			"review", "error_handling",
			"How should we handle this error?",
			"We should not ignore this error and return it to the caller with more information.",
			[]string{"error", "caller"},
			"Rather than swallowing the error, we should return it to the caller with context.",
			"The design document needs a new diagram.",
		},
		{
			"review", "scope_creep",
			"This pull request is getting big.",
			"I agree it is too big and we should move the refactor to another pull request.",
			[]string{"pull request", "refactor"},
			"Agreed — the refactor belongs in a separate pull request.",
			"My laptop battery drains very fast.",
		},
		{
			"review", "test_coverage",
			"Should we ask for more tests?",
			"Yes, I think this change needs a test for the empty input case.",
			[]string{"test", "edge case"},
			"Yes — this change needs a test covering the empty input case.",
			"The cafeteria closes at eight in the evening.",
		},

		// --- 1on1 ---
		{
			"1on1", "growth_plan",
			"What would you like to grow into this half?",
			"I want to grow into a tech lead and I need more chances to design systems.",
			[]string{"tech lead", "design"},
			"I would like to move toward tech lead, which means more chances to own system design.",
			"I am planning a trip to Yunnan next month.",
		},
		{
			"1on1", "workload",
			"How is your workload lately?",
			"I feel the workload is too much and I cannot finish everything in the sprint.",
			[]string{"workload", "sprint"},
			"My plate is too full — I cannot finish everything within the sprint.",
			"The new keyboard I bought is very quiet.",
		},
		{
			"1on1", "feedback",
			"Do you have any feedback for me?",
			"I hope we can have more regular feedback because I do not know if I am doing well.",
			[]string{"feedback", "regular"},
			"I would benefit from more regular feedback — right now I cannot tell how I am doing.",
			"Let us have lunch at the new noodle place.",
		},
		{
			"1on1", "transfer_request",
			"Any change you want to discuss?",
			"I want to move to the platform team because I am more interested in infrastructure.",
			[]string{"platform team", "infrastructure"},
			"I would like to transfer to the platform team — infrastructure is where my interest is.",
			"My phone screen has a small scratch.",
		},

		// --- interview ---
		{
			"interview", "self_intro",
			"Tell me about yourself.",
			"I am a backend engineer with four years of experience and I work on payment systems.",
			[]string{"backend", "experience"},
			"I am a backend engineer with four years of experience, mostly on payment systems.",
			"Could you repeat the question a bit slower?",
		},
		{
			"interview", "system_design",
			"How would you design a rate limiter?",
			"I would use a token bucket and store the counters in Redis with a short expire time.",
			[]string{"token bucket", "redis"},
			"A token bucket with counters in Redis and a short expiry is how I would build it.",
			"I have never used a database before.",
		},
		{
			"interview", "conflict",
			"Tell me about a disagreement with a colleague.",
			"Once my teammate and I disagreed about the API shape and we solved it with a design doc.",
			[]string{"disagree", "design doc"},
			"My teammate and I disagreed on the API shape; a design doc settled it.",
			"I usually avoid talking to my colleagues.",
		},
		{
			"interview", "why_leaving",
			"Why are you looking for a change?",
			"I want to work on bigger scale systems and my current team is very small.",
			[]string{"scale", "team"},
			"I am looking for larger-scale systems; my current team stays small.",
			"I would rather not answer any questions today.",
		},

		// --- casual ---
		{
			"casual", "weekend",
			"What did you do last weekend?",
			"I went to a small village near the city and walked around the old streets.",
			[]string{"weekend", "village"},
			"I spent the weekend in a small village outside the city, wandering the old streets.",
			"I deployed three services on Saturday night.",
		},
		{
			"casual", "coffee_chat",
			"Do you drink coffee?",
			"I drink two cups every day but I am trying to drink less now.",
			[]string{"coffee", "less"},
			"Two cups a day for me, though I am trying to cut back.",
			"Our sprint review is scheduled for Thursday.",
		},
		{
			"casual", "commute",
			"How long is your commute?",
			"My commute takes forty minutes by subway and I listen to podcasts on the way.",
			[]string{"commute", "podcast"},
			"Forty minutes by subway, which I spend listening to podcasts.",
			"The production database is running out of disk.",
		},
		{
			"casual", "sports",
			"Do you play any sports?",
			"I play badminton twice a week with friends from my old company.",
			[]string{"badminton", "friends"},
			"Badminton twice a week with friends from my previous company.",
			"I need to finish the quarterly report by Friday.",
		},
	}
}

// profile is one speaking defect applied to the frame's clean answer.
type profile struct {
	name string
}

func profiles() []profile {
	return []profile{
		{"clean"},
		{"filler_heavy"},
		{"incomplete"},
		{"silent_rescued"},
		{"l1_transfer"},
	}
}

// sample is one mock session plus the expectations the flow checks it against.
type sample struct {
	ID           string        `json:"id"`
	Scene        string        `json:"scene"`
	Profile      string        `json:"profile"`
	Topic        string        `json:"topic"`
	Language     string        `json:"language"`
	Utterances   []utterance   `json:"utterances"`
	RescueEvents []rescueEvent `json:"rescue_events,omitempty"`
	Expectations expectations  `json:"expectations"`
}

type utterance struct {
	Seq     int    `json:"seq"`
	Speaker string `json:"speaker"`
	Text    string `json:"text"`
}

type rescueEvent struct {
	Seq        int    `json:"seq"`
	TurnID     string `json:"turn_id"`
	Level      int    `json:"level"`
	Path       string `json:"path"`
	Ladder     string `json:"ladder,omitempty"`
	UserOpened bool   `json:"user_opened"`
	Anchor     string `json:"anchor,omitempty"`
}

type expectations struct {
	MinBlocks int      `json:"min_blocks"`
	MaxBlocks int      `json:"max_blocks"`
	Topics    []string `json:"topics"`
	// JudgeCases are gold labels for the flash-drill judge, authored against the
	// frame's topic so they stay meaningful whatever wording refine picks.
	JudgeCases []judgeCase `json:"judge_cases"`
	// HitCases are gold labels for the deterministic B7 detector.
	HitCases []hitCase `json:"hit_cases"`
}

type judgeCase struct {
	Answer string `json:"answer"`
	Expect string `json:"expect"` // "pass" | "fail"
}

type hitCase struct {
	Text   string `json:"text"`
	Expect string `json:"expect"` // "hit" | "miss"
	// Kind separates the gated checks from the informational one:
	//   offtopic   — must not hit (the error PRD §5.2.3 pays most to avoid)
	//   paraphrase — a same-meaning rewrite; the detector is deliberately
	//                conservative, so recall here is reported, not gated
	Kind string `json:"kind"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "gen-moat-eval-dataset FAILED: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	out := "eval/moat/dataset.json"
	if len(os.Args) > 1 {
		out = os.Args[1]
	}
	var samples []sample
	seq := 0
	for _, f := range frames() {
		for _, p := range profiles() {
			seq++
			samples = append(samples, buildSample(seq, f, p))
		}
	}
	if err := os.MkdirAll(filepath.Dir(out), 0o755); err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(map[string]any{
		"version": 1,
		"samples": samples,
	}, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(out, append(encoded, '\n'), 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %d samples to %s\n", len(samples), out)
	return nil
}

func buildSample(n int, f frame, p profile) sample {
	id := fmt.Sprintf("s%03d-%s-%s", n, f.topic, p.name)
	s := sample{
		ID:       id,
		Scene:    f.scene,
		Profile:  p.name,
		Topic:    f.topic,
		Language: "en",
	}
	switch p.name {
	case "clean":
		s.Utterances = []utterance{
			{1, "ai", f.ai},
			{2, "user", f.answer},
		}
	case "filler_heavy":
		s.Utterances = []utterance{
			{1, "ai", f.ai},
			{2, "user", withFillers(f.answer)},
		}
	case "incomplete":
		half := truncate(f.answer)
		s.Utterances = []utterance{
			{1, "ai", f.ai},
			{2, "user", half},
		}
		// The incomplete path: the anchor is the half-sentence itself, and the
		// ladder supplies the expression.
		s.RescueEvents = []rescueEvent{{
			Seq: 1, TurnID: "turn-1", Level: 3, Path: "incomplete",
			Ladder: f.answer, UserOpened: true, Anchor: half,
		}}
	case "silent_rescued":
		// The user says nothing until the ladder finishes, then answers.
		s.Utterances = []utterance{
			{1, "ai", f.ai},
			{2, "user", f.answer},
		}
		s.RescueEvents = []rescueEvent{
			{Seq: 1, TurnID: "turn-1", Level: 1, Path: "silent"},
			{Seq: 2, TurnID: "turn-1", Level: 2, Path: "silent"},
			{
				Seq: 3, TurnID: "turn-1", Level: 3, Path: "silent",
				Ladder: f.answer, UserOpened: true, Anchor: f.answer,
			},
		}
	case "l1_transfer":
		s.Utterances = []utterance{
			{1, "ai", f.ai},
			{2, "user", l1Transfer(f.answer)},
		}
	}
	s.Expectations = expectations{
		MinBlocks: 1,
		MaxBlocks: 5,
		Topics:    f.topics,
		JudgeCases: []judgeCase{
			{Answer: f.paraphrase, Expect: "pass"},
			{Answer: f.offTopic, Expect: "fail"},
		},
		HitCases: []hitCase{
			// A rewrite that means the same thing. Recall here is informational:
			// the detector trades it away on purpose (threshold 0.65).
			{Text: f.paraphrase, Expect: "hit", Kind: "paraphrase"},
			// Something unrelated: it must never hit.
			{Text: f.offTopic, Expect: "miss", Kind: "offtopic"},
		},
	}
	return s
}

// withFillers injects the disfluency a learner produces when thinking in
// Chinese and speaking in English.
func withFillers(answer string) string {
	words := strings.Fields(answer)
	var b strings.Builder
	fillers := []string{"uh, ", "you know, ", "I mean, ", "like, "}
	for i, w := range words {
		if i > 0 && i%5 == 0 {
			b.WriteString(fillers[(i/5)%len(fillers)])
		}
		b.WriteString(w)
		b.WriteString(" ")
	}
	return strings.TrimSpace(b.String())
}

// truncate cuts the answer where a learner runs out of words: mid-clause, on a
// connective, which is exactly PRD §5.4.1's 未完成句.
func truncate(answer string) string {
	idx := strings.Index(answer, " because ")
	if idx < 0 {
		idx = strings.Index(answer, " and ")
	}
	if idx < 0 {
		idx = strings.Index(answer, " so ")
	}
	if idx < 0 {
		words := strings.Fields(answer)
		if len(words) > 5 {
			words = words[:5]
		}
		return strings.Join(words, " ") + " and"
	}
	return strings.TrimSpace(answer[:idx]) + " because"
}

// l1Transfer rewrites the answer the way a Chinese speaker maps it word for word.
func l1Transfer(answer string) string {
	replacements := []struct{ from, to string }{
		{"I am blocked on", "I very blocked by"},
		{"I think", "I think that is"},
		{"I want to", "I want"},
		{"we should", "we must should"},
		{"I would", "I will want to"},
		{"I drink", "I very like drink"},
		{"I play", "I very like play"},
		{"The deploy is waiting", "The deploy is by migration waiting"},
	}
	out := answer
	for _, r := range replacements {
		if strings.Contains(out, r.from) {
			out = strings.Replace(out, r.from, r.to, 1)
			break
		}
	}
	return out
}
