// Command eval-moat-flow runs the moat core chain over a mock dataset and scores
// what comes out.
//
// The chain under test is the product's spine, and every stage is the production
// code path — the same prompts, the same thresholds, the same validators:
//
//	mock session → 回顾+炼化 → 入库 → 闪测判定 → 命中检测 → 话题卡
//
// Scoring is three-layered (docs/87): L1 rule checks that are machine-verifiable,
// L2 an LLM referee with an explicit rubric, and L3 a review sheet for humans to
// calibrate the referee. A quality number nobody can verify is an opinion; a
// quality number with a rubric and a gate is a product claim.
//
//	set -a && source .env.volc.local && set +a
//	APP_ENV=development go run ./cmd/eval-moat-flow --limit 10
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/aicost"
	"github.com/FluentWork/fluentwork-backend/internal/config"
	"github.com/FluentWork/fluentwork-backend/internal/corpus"
	"github.com/FluentWork/fluentwork-backend/internal/drill"
	"github.com/FluentWork/fluentwork-backend/internal/eval"
	"github.com/FluentWork/fluentwork-backend/internal/orchestrator"
	"github.com/FluentWork/fluentwork-backend/internal/reviewgen"
	"github.com/FluentWork/fluentwork-backend/internal/session"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "eval-moat-flow FAILED: %v\n", err)
		os.Exit(1)
	}
}

type options struct {
	dataset       string
	outDir        string
	limit         int
	concurrency   int
	reuseRun      string
	baseline      string
	writeBaseline string
}

func run() error {
	opts := options{}
	flag.StringVar(&opts.dataset, "dataset", "eval/moat/dataset.json", "path to the mock dataset")
	flag.StringVar(&opts.outDir, "out", "eval-out", "directory to write run artifacts into")
	flag.IntVar(&opts.limit, "limit", 0, "only run the first N samples (0 = all)")
	flag.IntVar(&opts.concurrency, "concurrency", 6, "parallel samples")
	flag.StringVar(&opts.reuseRun, "reuse-run", "",
		"reuse a previous run's samples/ directory and only re-run the topic stage")
	flag.StringVar(&opts.baseline, "baseline", "",
		"compare this run against a baseline (eval/moat/baseline.json)")
	flag.StringVar(&opts.writeBaseline, "write-baseline", "",
		"record this run as the baseline at the given path, then exit 0")
	flag.Parse()

	if strings.TrimSpace(os.Getenv("APP_ENV")) == "" {
		_ = os.Setenv("APP_ENV", "development")
	}
	cfg := config.Load()
	if !cfg.IsDevelopment() {
		return fmt.Errorf("this harness makes real model calls; run it with APP_ENV=development")
	}
	if strings.TrimSpace(cfg.ArkAPIKey) == "" {
		return fmt.Errorf("ARK_API_KEY / ARK_API_KEY_DEV is required (source .env.volc.local first)")
	}

	dataset, err := loadDataset(opts.dataset)
	if err != nil {
		return err
	}
	samples := dataset.Samples
	if opts.limit > 0 && opts.limit < len(samples) {
		samples = samples[:opts.limit]
	}

	// The cost store follows the deployment: with MYSQL_DSN set the run's calls
	// land in ai_cost_logs where they can be reconciled with SQL, which is the
	// point of recording them through the production path at all.
	// Price the run the way a deployment would: with the operator's table when
	// one is configured, so the money column in the ledger is produced by the
	// same code path production uses.
	if cfg.ArkPricingFile != "" {
		if err := orchestrator.LoadPricingFile(cfg.ArkPricingFile); err != nil {
			return fmt.Errorf("pricing file: %w", err)
		}
		fmt.Printf("pricing table loaded from %s\n", cfg.ArkPricingFile)
	}

	costStore, closeCost, err := aicost.OpenStore(cfg, slog.Default())
	if err != nil {
		return err
	}
	defer func() { _ = closeCost() }()
	costSvc := aicost.NewService(costStore, nil)
	client := orchestrator.NewClient(cfg, orchestrator.NewAICostWriterAdapter(costSvc))
	deps := &deps{
		client:   client,
		reviewer: &reviewgen.OrchestratorAdapter{Client: client},
		judge:    &drill.LLMJudge{LLM: &drill.OrchestratorAdapter{Client: client}},
		budget:   newJudgeBudget(client),
		referee:  &referee{client: client},
	}

	runID := time.Now().UTC().Format("20060102-150405")
	outDir := filepath.Join(opts.outDir, runID)
	if err := os.MkdirAll(filepath.Join(outDir, "samples"), 0o755); err != nil {
		return err
	}

	started := time.Now()
	var results []*sampleResult
	if opts.reuseRun != "" {
		// Rebuilding from a finished run's artifacts means a stage can be
		// iterated without re-paying for the others: the per-sample files carry
		// every stage's input and output.
		reused, err := loadPreviousResults(opts.reuseRun)
		if err != nil {
			return err
		}
		results = reused
		fmt.Printf("reusing %d samples from %s; running the topic stage only → %s\n",
			len(results), opts.reuseRun, outDir)
	} else {
		fmt.Printf("running %d samples (concurrency %d) → %s\n", len(samples), opts.concurrency, outDir)
		results = runAll(context.Background(), deps, samples, opts.concurrency)
	}
	// The topic stage runs per scene group, not per sample: H1 gates topic cards
	// on a corpus of >= 20 blocks, so judging them from a single mock session's
	// one or two blocks would measure the generator padding to fill three cards
	// rather than the product's behaviour.
	topics := runTopicStage(context.Background(), deps, samples, results)
	elapsed := time.Since(started)

	report := aggregate(runID, results, topics, elapsed, costSvc)

	if opts.writeBaseline != "" {
		if !report.Valid {
			return fmt.Errorf("refusing to record an invalid run as the baseline: %s", report.InvalidReason)
		}
		if err := writeBaseline(opts.writeBaseline, report); err != nil {
			return err
		}
		fmt.Printf("baseline written to %s\n", opts.writeBaseline)
	}
	if err := writeArtifacts(outDir, results, topics, report); err != nil {
		return err
	}
	if opts.baseline != "" {
		base, err := loadBaseline(opts.baseline)
		if err != nil {
			return err
		}
		regressions, table := compareToBaseline(base, report)
		if err := os.WriteFile(filepath.Join(outDir, "baseline-diff.md"), []byte(table), 0o644); err != nil {
			return err
		}
		fmt.Printf("\n=== 与基线对比（%s）===\n%s", opts.baseline, table)
		if len(regressions) > 0 {
			names := make([]string, 0, len(regressions))
			for _, r := range regressions {
				names = append(names, fmt.Sprintf("%s %.3f→%.3f", r.Name, r.Baseline, r.Current))
			}
			return fmt.Errorf("regressed against the baseline: %s", strings.Join(names, "; "))
		}
	}

	printSummary(report, outDir)
	if !report.Valid {
		return fmt.Errorf("run is INVALID, not a quality result: %s (%s)", report.InvalidReason, strings.Join(report.FirstErrors, " | "))
	}
	if !report.Gate.Passed {
		return fmt.Errorf("quality gate FAILED: %s", strings.Join(report.Gate.Reasons, "; "))
	}
	return nil
}

type deps struct {
	client   orchestrator.Client
	reviewer *reviewgen.OrchestratorAdapter
	judge    *drill.LLMJudge
	budget   *judgeBudget
	referee  *referee
}

func runAll(ctx context.Context, deps *deps, samples []sample, concurrency int) []*sampleResult {
	results := make([]*sampleResult, len(samples))
	if concurrency < 1 {
		concurrency = 1
	}
	sem := make(chan struct{}, concurrency)
	var wg sync.WaitGroup
	for i, s := range samples {
		wg.Add(1)
		go func(i int, s sample) {
			defer wg.Done()
			sem <- struct{}{}
			defer func() { <-sem }()
			results[i] = runSample(ctx, deps, s)
		}(i, s)
	}
	wg.Wait()
	return results
}

// runSample walks one mock session through the chain, capturing what each stage
// received and what it produced.
func runSample(ctx context.Context, deps *deps, s sample) *sampleResult {
	res := &sampleResult{Sample: s}
	userID := "eval-user-" + s.ID
	sessionID := "eval-session-" + s.ID
	transcript := renderTranscript(s.Utterances)

	// --- ① 回顾 + 炼化 ---------------------------------------------------
	genResult, err := deps.reviewer.Generate(ctx, reviewgen.Request{
		SessionID:   sessionID,
		UserID:      userID,
		SceneType:   s.Scene,
		Transcript:  transcript,
		StuckEvents: stuckEvents(s),
	})
	if err != nil {
		res.Errors = append(res.Errors, "refine: "+err.Error())
		return res
	}
	res.Refine = refineStage{
		ReviewJSON: string(genResult.Review),
		RefineJSON: string(genResult.Refine),
		Model:      genResult.Model,
		TokensIn:   genResult.TokensIn,
		TokensOut:  genResult.TokensOut,
	}
	res.Refine.Findings = eval.ValidateSample(eval.Sample{
		ID: s.ID, Transcript: transcript, Review: genResult.Review, Refine: genResult.Refine,
	})
	blocks := parseRefineBlocks(genResult.Refine)
	res.Refine.Blocks = blocks

	// --- ② 入库 ----------------------------------------------------------
	corpusStore := corpus.NewMemoryStore()
	corpusSvc := corpus.NewService(corpusStore, nil)
	accept, err := corpusSvc.BatchAccept(ctx, userID, corpus.BatchAcceptRequest{
		SourceSessionID: sessionID,
		Blocks:          toAcceptBlocks(blocks),
	})
	if err != nil {
		res.Errors = append(res.Errors, "batch-accept: "+err.Error())
		return res
	}
	res.Corpus = corpusStage{AcceptedCount: accept.AcceptedCount, MergedCount: accept.MergedCount}
	for _, item := range accept.Items {
		res.Corpus.BlockIDs = append(res.Corpus.BlockIDs, item.ID)
		res.Corpus.Expressions = append(res.Corpus.Expressions, item.ExpressionEN)
	}

	// --- L1 rule checks --------------------------------------------------
	res.Checks = ruleChecks(s, transcript, blocks, accept.Items)

	// --- ③ 闪测判定 -------------------------------------------------------
	// Two passes per case: the production budget (a latency health signal) and a
	// generous one (the accuracy measurement). A timed-out judge answers "fail",
	// so scoring its output as a verdict would silently manufacture accuracy.
	for _, tc := range s.Expectations.JudgeCases {
		target, ok := pickTargetBlock(blocks, s.Expectations.Topics)
		if !ok {
			res.Judge = append(res.Judge, judgeOutcome{
				Answer: tc.Answer, Expect: tc.Expect, Skipped: "no block matched the sample topics",
			})
			continue
		}
		outcome := judgeOutcome{Answer: tc.Answer, Expect: tc.Expect, Target: target.ExpressionEN}

		prod, prodElapsed := judgeUnderProductionBudget(ctx, deps.judge, target.ExpressionEN, tc.Answer)
		outcome.ProdDurationMS = prodElapsed.Milliseconds()
		outcome.ProdTimedOut = !prod.Judged || prodElapsed >= deps.judge.Budget()

		scored, judgeElapsed, err := deps.budget.judgeWithRoom(ctx, target.ExpressionEN, tc.Answer)
		switch {
		case err != nil:
			outcome.Error = err.Error()
		case !scored.Judged:
			outcome.Error = "accuracy pass could not judge: " + scored.Reason
		default:
			outcome.Got = passFail(scored.Pass)
			outcome.Reason = scored.Reason
			outcome.Correct = outcome.Got == tc.Expect
			outcome.JudgeDurationMS = judgeElapsed.Milliseconds()
		}
		res.Judge = append(res.Judge, outcome)
	}

	// --- ④ 命中检测 -------------------------------------------------------
	// The gated cases are the two that matter: the block's own expression must
	// hit, and an unrelated sentence must not. The frame's paraphrase is
	// recorded as informational recall — the detector is deliberately
	// conservative (PRD §5.2.3: 宁可漏报不可错报), so gating recall would push
	// the threshold the wrong way.
	detector := session.NewHitDetector(corpus.NewBlockSourceAdapter(corpusSvc))
	hitTarget, hasTarget := pickTargetBlock(blocks, s.Expectations.Topics)
	if hasTarget {
		decision, err := detector.Detect(ctx, session.HitDetectRequest{
			UserID: userID, SessionID: sessionID, TurnID: "turn-self", Text: hitTarget.ExpressionEN,
		})
		outcome := hitOutcome{Text: hitTarget.ExpressionEN, Expect: "hit", Kind: "self"}
		if err != nil {
			outcome.Error = err.Error()
		} else {
			outcome.Got = "miss"
			if decision.Kind == session.HitDecisionHit {
				outcome.Got = "hit"
			}
			outcome.Correct = outcome.Got == "hit"
		}
		res.Hit = append(res.Hit, outcome)
	}
	for _, hc := range s.Expectations.HitCases {
		decision, err := detector.Detect(ctx, session.HitDetectRequest{
			UserID: userID, SessionID: sessionID, TurnID: "turn-" + hc.Kind, Text: hc.Text,
		})
		outcome := hitOutcome{Text: hc.Text, Expect: hc.Expect, Kind: hc.Kind}
		if err != nil {
			outcome.Error = err.Error()
		} else {
			outcome.Got = "miss"
			if decision.Kind == session.HitDecisionHit {
				outcome.Got = "hit"
			}
			outcome.Correct = outcome.Got == hc.Expect
		}
		res.Hit = append(res.Hit, outcome)
	}

	// --- L2 LLM 裁判 ------------------------------------------------------
	scores, err := deps.referee.score(ctx, refereeInput{
		Scene: s.Scene, Transcript: transcript, Blocks: blocks,
		ReviewJSON: string(genResult.Review),
	})
	if err != nil {
		res.Errors = append(res.Errors, "referee: "+err.Error())
	} else {
		res.Scores = scores
	}
	return res
}

func stuckEvents(s sample) []reviewgen.StuckEvent {
	out := make([]reviewgen.StuckEvent, 0, len(s.RescueEvents))
	for _, e := range s.RescueEvents {
		out = append(out, reviewgen.StuckEvent{
			Seq: e.Seq, TurnID: e.TurnID, Level: e.Level, Path: e.Path,
			Ladder: e.Ladder, UserOpened: e.UserOpened, Anchor: e.Anchor,
		})
	}
	return out
}

func renderTranscript(utterances []utterance) string {
	var b strings.Builder
	for _, u := range utterances {
		fmt.Fprintf(&b, "%s: %s\n", u.Speaker, u.Text)
	}
	return strings.TrimSpace(b.String())
}

func passFail(pass bool) string {
	if pass {
		return "pass"
	}
	return "fail"
}

// pickTargetBlock chooses the block a judge case is about: the one whose
// expression touches the most of the sample's topic keywords. When nothing
// matches, the case is skipped rather than scored against the wrong target —
// a mis-targeted accuracy number is worse than a missing one.
func pickTargetBlock(blocks []refineBlock, topics []string) (refineBlock, bool) {
	best := refineBlock{}
	bestHits := 0
	for _, b := range blocks {
		hits := 0
		lower := strings.ToLower(b.ExpressionEN)
		for _, t := range topics {
			if strings.Contains(lower, strings.ToLower(t)) {
				hits++
			}
		}
		if hits > bestHits {
			best, bestHits = b, hits
		}
	}
	return best, bestHits > 0
}

func toAcceptBlocks(blocks []refineBlock) []corpus.BatchAcceptBlock {
	out := make([]corpus.BatchAcceptBlock, 0, len(blocks))
	for _, b := range blocks {
		out = append(out, corpus.BatchAcceptBlock{
			IntentZH: b.IntentZH, ExpressionEN: b.ExpressionEN, AnchorUserSaid: b.AnchorUserSaid,
			SceneTag: b.SceneTag, FunctionTag: b.FunctionTag,
		})
	}
	return out
}

// --- dataset types ------------------------------------------------------

type dataset struct {
	Version int      `json:"version"`
	Samples []sample `json:"samples"`
}

type sample struct {
	ID           string        `json:"id"`
	Scene        string        `json:"scene"`
	Profile      string        `json:"profile"`
	Topic        string        `json:"topic"`
	Utterances   []utterance   `json:"utterances"`
	RescueEvents []rescueEvent `json:"rescue_events"`
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
	Ladder     string `json:"ladder"`
	UserOpened bool   `json:"user_opened"`
	Anchor     string `json:"anchor"`
}

type expectations struct {
	MinBlocks  int         `json:"min_blocks"`
	MaxBlocks  int         `json:"max_blocks"`
	Topics     []string    `json:"topics"`
	JudgeCases []judgeCase `json:"judge_cases"`
	HitCases   []hitCase   `json:"hit_cases"`
}

type judgeCase struct {
	Answer string `json:"answer"`
	Expect string `json:"expect"`
}

type hitCase struct {
	Text   string `json:"text"`
	Expect string `json:"expect"`
	Kind   string `json:"kind"`
}

// refineBlock is one 三元组 as the model returned it.
type refineBlock struct {
	IntentZH       string `json:"intent_zh"`
	ExpressionEN   string `json:"expression_en"`
	AnchorUserSaid string `json:"anchor_user_said"`
	SceneTag       string `json:"scene_tag"`
	FunctionTag    string `json:"function_tag"`
}

func parseRefineBlocks(raw json.RawMessage) []refineBlock {
	var doc struct {
		Blocks []refineBlock `json:"blocks"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil
	}
	return doc.Blocks
}

// loadPreviousResults reads a finished run's per-sample artifacts.
func loadPreviousResults(dir string) ([]*sampleResult, error) {
	paths, err := filepath.Glob(filepath.Join(dir, "samples", "*.json"))
	if err != nil {
		return nil, err
	}
	if len(paths) == 0 {
		return nil, fmt.Errorf("no samples found under %s/samples", dir)
	}
	sort.Strings(paths)
	out := make([]*sampleResult, 0, len(paths))
	for _, path := range paths {
		raw, err := os.ReadFile(path)
		if err != nil {
			return nil, err
		}
		var res sampleResult
		if err := json.Unmarshal(raw, &res); err != nil {
			return nil, fmt.Errorf("parse %s: %w", path, err)
		}
		out = append(out, &res)
	}
	return out, nil
}

func loadDataset(path string) (dataset, error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return dataset{}, fmt.Errorf("read dataset: %w", err)
	}
	var doc dataset
	if err := json.Unmarshal(raw, &doc); err != nil {
		return dataset{}, fmt.Errorf("parse dataset: %w", err)
	}
	if len(doc.Samples) == 0 {
		return dataset{}, fmt.Errorf("dataset %s has no samples", path)
	}
	sort.SliceStable(doc.Samples, func(i, j int) bool { return doc.Samples[i].ID < doc.Samples[j].ID })
	return doc, nil
}
