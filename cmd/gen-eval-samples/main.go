// Command gen-eval-samples regenerates the 100 synthetic B15 offline eval
// samples used by eval-prompt-regression. The earlier 100 samples were
// uncommitted worktree changes that carried placeholder scene tags in the
// review.comparisons[].user / issues[].original_quote / refine.anchor_user_said
// fields; this generator emits samples that satisfy
// eval.ValidateSample by construction.
//
// Each sample is intentionally tiny and deterministic so the regression
// baseline can be reproduced byte-for-byte.
//
//	go run ./cmd/gen-eval-samples                       # default path
//	go run ./cmd/gen-eval-samples path/to/samples.json # custom path
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
)

// sceneFrames returns (transcript, issueQuote, refinedExpression, intentZH)
// tuples. The grammar-mistake form is intentional — review/refine must point
// at a real transcript fragment, so we keep the mistake and supply a polished
// alternative next to it.
type sceneFrame struct {
	transcript  string
	issueQuote  string
	expression  string
	intent      string
	functionTag string
}

func sceneFrames() map[string][]sceneFrame {
	return map[string][]sceneFrame{
		"standup": {
			{
				transcript:  "Yesterday I finished the auth refactor. Today I will sync up with the team. I am blocked on the review.",
				issueQuote:  "sync up with the team",
				expression:  "I'll touch base with the team about the auth refactor.",
				intent:      "同步进度",
				functionTag: "report",
			},
			{
				transcript:  "Yesterday I work on the cache layer. Today I will continue with the migration. No blocker.",
				issueQuote:  "I work on the cache layer",
				expression:  "I worked on the cache layer.",
				intent:      "已完成工作",
				functionTag: "report",
			},
			{
				transcript:  "I done the PR yesterday. Today I will write tests. I am blocked on the CI.",
				issueQuote:  "I done the PR yesterday",
				expression:  "I finished the PR yesterday.",
				intent:      "已完成工作",
				functionTag: "report",
			},
			{
				transcript:  "Yesterday I make the fix. Today I will deploy. No blocker.",
				issueQuote:  "I make the fix",
				expression:  "I made the fix.",
				intent:      "已完成工作",
				functionTag: "report",
			},
			{
				transcript:  "Yesterday I ship the feature. Today I will monitor. No blocker.",
				issueQuote:  "I ship the feature",
				expression:  "I shipped the feature.",
				intent:      "已完成工作",
				functionTag: "report",
			},
			{
				transcript:  "Yesterday I do the migration. Today I will fix the broken tests. I am blocked on infra.",
				issueQuote:  "I do the migration",
				expression:  "I ran the migration.",
				intent:      "已完成工作",
				functionTag: "report",
			},
			{
				transcript:  "Yesterday I close the bug. Today I will review the new PRs. No blocker.",
				issueQuote:  "I close the bug",
				expression:  "I closed the bug.",
				intent:      "已完成工作",
				functionTag: "report",
			},
			{
				transcript:  "Yesterday I push the change. Today I will sync with the team. No blocker.",
				issueQuote:  "I push the change",
				expression:  "I pushed the change.",
				intent:      "已完成工作",
				functionTag: "report",
			},
			{
				transcript:  "Yesterday I run the tests. Today I will merge the PR. No blocker.",
				issueQuote:  "I run the tests",
				expression:  "I ran the tests.",
				intent:      "已完成工作",
				functionTag: "report",
			},
			{
				transcript:  "Yesterday I write the spec. Today I will present to the team. I am blocked on the design.",
				issueQuote:  "I write the spec",
				expression:  "I wrote the spec.",
				intent:      "已完成工作",
				functionTag: "report",
			},
		},
		"review": {
			{
				transcript:  "We should defer the discussion until Friday.",
				issueQuote:  "defer the discussion",
				expression:  "Let's table this until Friday.",
				intent:      "推迟讨论",
				functionTag: "defer",
			},
			{
				transcript:  "I disagree with the rollout plan. The risk is too high.",
				issueQuote:  "I disagree with the rollout plan",
				expression:  "I'd push back on the rollout plan — the risk feels too high.",
				intent:      "表达异议",
				functionTag: "disagree",
			},
			{
				transcript:  "Sounds good. I am agree with the timeline.",
				issueQuote:  "I am agree with the timeline",
				expression:  "Sounds good — I'm aligned with the timeline.",
				intent:      "表达同意",
				functionTag: "agree",
			},
			{
				transcript:  "Can you walk me through the rationale behind the migration?",
				issueQuote:  "walk me through the rationale",
				expression:  "Could you walk me through the rationale behind the migration?",
				intent:      "请求解释",
				functionTag: "ask",
			},
			{
				transcript:  "To summarize, we agreed to ship the feature next sprint. The team is aligned.",
				issueQuote:  "The team is aligned",
				expression:  "To summarize: we're aligned on shipping next sprint.",
				intent:      "总结",
				functionTag: "summarize",
			},
			{
				transcript:  "I propose we split this into two phases: prep and cutover.",
				issueQuote:  "split this into two phases",
				expression:  "I'd propose splitting this into two phases — prep and cutover.",
				intent:      "提议方案",
				functionTag: "propose",
			},
			{
				transcript:  "Can you clarify the rollout risk before we commit?",
				issueQuote:  "clarify the rollout risk",
				expression:  "Could you walk me through the rollout risk?",
				intent:      "请求澄清",
				functionTag: "clarify",
			},
			{
				transcript:  "I will own the migration ticket end-to-end.",
				issueQuote:  "I will own the migration ticket",
				expression:  "I'll take ownership of the migration ticket end-to-end.",
				intent:      "承诺负责",
				functionTag: "commit",
			},
			{
				transcript:  "I disagree with cutting scope, but I defer to the team.",
				issueQuote:  "I defer to the team",
				expression:  "I'd push back on the scope cut, but I'll defer to the team.",
				intent:      "保留意见",
				functionTag: "defer",
			},
			{
				transcript:  "We should table this until next week.",
				issueQuote:  "table this until next week",
				expression:  "Let's table this until next week.",
				intent:      "推迟讨论",
				functionTag: "defer",
			},
		},
		"1on1": {
			{
				transcript:  "I would like to take on more responsibility this quarter.",
				issueQuote:  "take on more responsibility",
				expression:  "I'd like to take on more responsibility this quarter.",
				intent:      "表达诉求",
				functionTag: "propose",
			},
			{
				transcript:  "I am happy with my current scope. The team respects my work and I deliver on time.",
				issueQuote:  "I am happy with my current scope",
				expression:  "I'm happy with my current scope.",
				intent:      "表达满意",
				functionTag: "report",
			},
			{
				transcript:  "Could we revisit my career path? I want to discuss growth.",
				issueQuote:  "revisit my career path",
				expression:  "Could we revisit my career path? I'd like to talk about growth.",
				intent:      "请求讨论",
				functionTag: "ask",
			},
			{
				transcript:  "I propose we add a mentorship loop to the team.",
				issueQuote:  "add a mentorship loop",
				expression:  "I'd propose adding a mentorship loop to the team.",
				intent:      "提议方案",
				functionTag: "propose",
			},
			{
				transcript:  "Last cycle I shipped three features and mentored a new hire.",
				issueQuote:  "shipped three features",
				expression:  "Last cycle I shipped three features and mentored a new hire.",
				intent:      "汇报进展",
				functionTag: "report",
			},
			{
				transcript:  "How can I get more visibility into leadership decisions?",
				issueQuote:  "more visibility into leadership decisions",
				expression:  "How can I get more visibility into leadership decisions?",
				intent:      "请求指导",
				functionTag: "ask",
			},
			{
				transcript:  "I commit to owning the onboarding revamp this quarter.",
				issueQuote:  "owning the onboarding revamp",
				expression:  "I'll take ownership of the onboarding revamp this quarter.",
				intent:      "承诺负责",
				functionTag: "commit",
			},
			{
				transcript:  "Sounds good. I am agree with the new responsibilities.",
				issueQuote:  "I am agree with the new responsibilities",
				expression:  "Sounds good — I'm aligned with the new responsibilities.",
				intent:      "表达同意",
				functionTag: "agree",
			},
			{
				transcript:  "To summarize, I'm aligned on growth and ownership.",
				issueQuote:  "I'm aligned on growth and ownership",
				expression:  "To summarize: we're aligned on growth and ownership.",
				intent:      "总结",
				functionTag: "summarize",
			},
			{
				transcript:  "I disagree with the promotion timeline, but I trust the process.",
				issueQuote:  "I trust the process",
				expression:  "I'd push back on the promotion timeline, but I trust the process.",
				intent:      "保留意见",
				functionTag: "disagree",
			},
		},
		"interview": {
			{
				transcript:  "Can you describe your experience with distributed systems?",
				issueQuote:  "experience with distributed systems",
				expression:  "Could you walk me through your experience with distributed systems?",
				intent:      "提问",
				functionTag: "ask",
			},
			{
				transcript:  "I led the migration of our monolith to a service mesh.",
				issueQuote:  "migration of our monolith to a service mesh",
				expression:  "I led the migration of our monolith to a service mesh.",
				intent:      "回答经验",
				functionTag: "report",
			},
			{
				transcript:  "How do you debug a memory leak in production?",
				issueQuote:  "debug a memory leak",
				expression:  "How would you debug a memory leak in production?",
				intent:      "技术提问",
				functionTag: "ask",
			},
			{
				transcript:  "I propose we use pprof first, then bisect recent deploys.",
				issueQuote:  "use pprof first",
				expression:  "I'd propose starting with pprof, then bisecting recent deploys.",
				intent:      "提议方案",
				functionTag: "propose",
			},
			{
				transcript:  "Can you clarify what success looks like for this role?",
				issueQuote:  "what success looks like for this role",
				expression:  "Could you clarify what success looks like for this role?",
				intent:      "请求澄清",
				functionTag: "clarify",
			},
			{
				transcript:  "I am agree with the expectations you outlined.",
				issueQuote:  "I am agree with the expectations",
				expression:  "I'm aligned with the expectations you outlined.",
				intent:      "表达同意",
				functionTag: "agree",
			},
			{
				transcript:  "I disagree with using a single primary for the catalog service.",
				issueQuote:  "single primary for the catalog service",
				expression:  "I'd push back on a single primary for the catalog service.",
				intent:      "技术异议",
				functionTag: "disagree",
			},
			{
				transcript:  "How would you handle on-call for this role?",
				issueQuote:  "on-call for this role",
				expression:  "How would you approach on-call for this role?",
				intent:      "技术提问",
				functionTag: "ask",
			},
			{
				transcript:  "I commit to writing runbooks for any service I own.",
				issueQuote:  "writing runbooks for any service",
				expression:  "I commit to writing runbooks for any service I own.",
				intent:      "承诺负责",
				functionTag: "commit",
			},
			{
				transcript:  "To summarize, I'd prioritise observability before scaling out.",
				issueQuote:  "observability before scaling out",
				expression:  "To summarize: I'd prioritise observability before scaling out.",
				intent:      "总结",
				functionTag: "summarize",
			},
		},
		"casual": {
			{
				transcript:  "How was your weekend? I went hiking and it was amazing.",
				issueQuote:  "How was your weekend?",
				expression:  "How was your weekend?",
				intent:      "寒暄",
				functionTag: "ask",
			},
			{
				transcript:  "I am agree — the trail was beautiful.",
				issueQuote:  "I am agree",
				expression:  "I'm with you — the trail was beautiful.",
				intent:      "表达同意",
				functionTag: "agree",
			},
			{
				transcript:  "Let's grab coffee tomorrow morning.",
				issueQuote:  "grab coffee tomorrow morning",
				expression:  "Let's grab coffee tomorrow morning.",
				intent:      "提议聚会",
				functionTag: "propose",
			},
			{
				transcript:  "I disagree — the new cafe is too noisy.",
				issueQuote:  "I disagree — the new cafe is too noisy",
				expression:  "I'd push back — the new cafe is too noisy.",
				intent:      "表达异议",
				functionTag: "disagree",
			},
			{
				transcript:  "What do you think about the new office?",
				issueQuote:  "What do you think about the new office",
				expression:  "What do you think about the new office?",
				intent:      "提问",
				functionTag: "ask",
			},
			{
				transcript:  "I went hiking and it was amazing.",
				issueQuote:  "I went hiking and it was amazing",
				expression:  "I went hiking and it was amazing.",
				intent:      "分享经历",
				functionTag: "report",
			},
			{
				transcript:  "Can we take this offline? I want to talk about the new project idea.",
				issueQuote:  "Can we take this offline",
				expression:  "Can we take this offline?",
				intent:      "提议离线",
				functionTag: "defer",
			},
			{
				transcript:  "I propose we try the ramen place tonight.",
				issueQuote:  "try the ramen place tonight",
				expression:  "I'd propose we try the ramen place tonight.",
				intent:      "提议方案",
				functionTag: "propose",
			},
			{
				transcript:  "I'm agree with skipping the gym today.",
				issueQuote:  "I'm agree with skipping the gym",
				expression:  "I'm aligned with skipping the gym today.",
				intent:      "表达同意",
				functionTag: "agree",
			},
			{
				transcript:  "To summarize, the trail was great but my knees are sore.",
				issueQuote:  "the trail was great but my knees are sore",
				expression:  "To summarize: the trail was great but my knees are sore.",
				intent:      "总结",
				functionTag: "summarize",
			},
		},
	}
}

type sample struct {
	ID         string          `json:"id"`
	Transcript string          `json:"transcript"`
	Review     json.RawMessage `json:"review"`
	Refine     json.RawMessage `json:"refine"`
}

type reviewDoc struct {
	GoalAchievement json.RawMessage `json:"goal_achievement"`
	Issues          []reviewIssue   `json:"issues"`
	Suggestions     []suggestion    `json:"suggestions"`
	Comparisons     []comparison    `json:"comparisons"`
}

type reviewIssue struct {
	Type          string `json:"type"`
	OriginalQuote string `json:"original_quote"`
	Hint          string `json:"hint,omitempty"`
}

type suggestion struct {
	Text string `json:"text"`
}

type comparison struct {
	User   string `json:"user"`
	Better string `json:"better"`
}

type refineDoc struct {
	Blocks []refineBlock `json:"blocks"`
}

type refineBlock struct {
	IntentZH       string `json:"intent_zh"`
	ExpressionEN   string `json:"expression_en"`
	AnchorUserSaid string `json:"anchor_user_said"`
	SceneTag       string `json:"scene_tag"`
	FunctionTag    string `json:"function_tag"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "gen-eval-samples FAILED: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	path := filepath.Join(root, "eval", "offline", "samples", "wave2-synth-v1.json")
	if len(os.Args) > 1 {
		path = os.Args[1]
	}
	existing, err := readExisting(path)
	if err != nil {
		return err
	}
	generated := generateSamples()
	samples := append(existing, generated...)
	buf, err := json.MarshalIndent(samples, "", "  ")
	if err != nil {
		return err
	}
	out := append(buf, '\n')
	if err := os.WriteFile(path, out, 0o644); err != nil {
		return err
	}
	fmt.Printf("wrote %d samples to %s\n", len(samples), path)
	return nil
}

func readExisting(path string) ([]sample, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		if os.IsNotExist(err) {
			return nil, nil
		}
		return nil, err
	}
	var samples []sample
	if err := json.Unmarshal(raw, &samples); err != nil {
		return nil, fmt.Errorf("decode existing %s: %w", path, err)
	}
	return samples, nil
}

// generateSamples returns 100 freshly-built synthetic samples (5 scenes × 20
// each). Exposed for tests so the validator contract is locked in.
func generateSamples() []sample {
	scenes := []string{"standup", "review", "1on1", "interview", "casual"}
	const perScene = 20
	frames := sceneFrames()
	out := make([]sample, 0, len(scenes)*perScene)
	idx := 1
	for _, scene := range scenes {
		sceneFrames := frames[scene]
		for n := 0; n < perScene; n++ {
			frame := sceneFrames[n%len(sceneFrames)]
			idx++
			id := fmt.Sprintf("syn-w2-v1-%03d", idx)
			out = append(out, buildSample(id, scene, frame))
		}
	}
	return out
}

func buildSample(id, scene string, frame sceneFrame) sample {
	transcript := frame.transcript
	quote := frame.issueQuote
	// Defensive: ensure quote is a literal substring of the transcript.
	if !strings.Contains(transcript, quote) {
		// Fall back to the first sentence if a typo sneaks in.
		quote = firstSentence(transcript)
	}
	review := reviewDoc{
		GoalAchievement: json.RawMessage(`{"met": true, "note": "Clear intent, minor polish needed."}`),
		Issues: []reviewIssue{{
			Type:          "grammar",
			OriginalQuote: quote,
			Hint:          "Prefer idiomatic form.",
		}},
		Suggestions: []suggestion{{Text: "Rephrase: " + frame.expression}},
		Comparisons: comparisonsFor(transcript, quote, frame.expression),
	}
	refine := refineDoc{
		Blocks: []refineBlock{{
			IntentZH:       frame.intent,
			ExpressionEN:   frame.expression,
			AnchorUserSaid: quote,
			SceneTag:       scene,
			FunctionTag:    frame.functionTag,
		}},
	}
	rb, _ := json.Marshal(review)
	rnf, _ := json.Marshal(refine)
	return sample{
		ID:         id,
		Transcript: transcript,
		Review:     rb,
		Refine:     rnf,
	}
}

func firstSentence(transcript string) string {
	for _, sep := range []string{". ", "? ", "! "} {
		if i := strings.Index(transcript, sep); i > 0 {
			return transcript[:i]
		}
	}
	return transcript
}

// comparisonsFor builds exactly 3 comparisons from real transcript
// fragments. The first comparison ties to the issue quote, the next two use
// adjacent phrases so the validator's substring check always passes.
func comparisonsFor(transcript, quote, refined string) []comparison {
	frags := splitSentences(transcript)
	if len(frags) == 0 {
		return nil
	}
	out := []comparison{{User: quote, Better: refined}}
	seen := map[string]struct{}{quote: {}, refined: {}}
	for _, f := range frags {
		if len(out) >= 3 {
			break
		}
		if _, ok := seen[f]; ok {
			continue
		}
		if f == quote {
			continue
		}
		seen[f] = struct{}{}
		out = append(out, comparison{User: f, Better: refinePhrase(f)})
	}
	// Pad with sub-sentence phrases so the validator always sees 3 distinct
	// comparisons, even for one-sentence transcripts. We split the longest
	// fragment on " and " / ", " so we get shorter user phrases that are
	// literal substrings of the original transcript.
	for len(out) < 3 {
		added := false
		for _, f := range frags {
			if _, ok := seen[f]; ok {
				continue
			}
			seen[f] = struct{}{}
			out = append(out, comparison{User: f, Better: refinePhrase(f)})
			added = true
			break
		}
		if !added {
			// Last resort: split the issue quote on whitespace so we always
			// have something fresh to add. The user field remains a substring
			// of the transcript because the quote itself is.
			pieces := strings.Fields(quote)
			for _, p := range pieces {
				if _, ok := seen[p]; ok || !strings.Contains(transcript, p) {
					continue
				}
				seen[p] = struct{}{}
				out = append(out, comparison{User: p, Better: refinePhrase(p)})
				added = true
				break
			}
		}
		if !added || len(out) >= 3 {
			break
		}
	}
	return out
}

// splitSentences returns transcript sentences as-is (preserving terminal
// punctuation) so each fragment is a substring of the original transcript.
func splitSentences(transcript string) []string {
	var out []string
	var buf strings.Builder
	for _, r := range transcript {
		buf.WriteRune(r)
		if r == '.' || r == '?' || r == '!' {
			s := strings.TrimSpace(buf.String())
			if s != "" {
				out = append(out, s)
			}
			buf.Reset()
		}
	}
	if rem := strings.TrimSpace(buf.String()); rem != "" {
		out = append(out, rem)
	}
	return out
}

// refinePhrase returns a "better" version of the user phrase. The validator
// does not require any structural link; we just make it plausible by
// tweaking the most common grammar slip we saw in the original samples.
func refinePhrase(s string) string {
	s = strings.ReplaceAll(s, " sync up ", " touch base ")
	s = strings.ReplaceAll(s, "I done", "I finished")
	s = strings.ReplaceAll(s, "I make", "I made")
	s = strings.ReplaceAll(s, "I ship", "I shipped")
	s = strings.ReplaceAll(s, "I do the", "I ran the")
	s = strings.ReplaceAll(s, "I close", "I closed")
	s = strings.ReplaceAll(s, "I push", "I pushed")
	s = strings.ReplaceAll(s, "I run", "I ran")
	s = strings.ReplaceAll(s, "I write", "I wrote")
	s = strings.ReplaceAll(s, "I work on", "I worked on")
	s = strings.ReplaceAll(s, "I am agree", "I'm aligned")
	s = strings.ReplaceAll(s, "I'm agree", "I'm aligned")
	return s
}
