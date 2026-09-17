package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/aicost"
	"github.com/FluentWork/fluentwork-backend/internal/corpus"
	"github.com/FluentWork/fluentwork-backend/internal/drill"
	"github.com/FluentWork/fluentwork-backend/internal/eval"
)

// --- per-sample result --------------------------------------------------

type sampleResult struct {
	Sample sample         `json:"sample"`
	Refine refineStage    `json:"refine"`
	Corpus corpusStage    `json:"corpus"`
	Checks []check        `json:"checks"`
	Judge  []judgeOutcome `json:"judge"`
	Hit    []hitOutcome   `json:"hit"`
	Topic  []topicOutcome `json:"topic"`
	Scores *rubricVerdict `json:"rubric,omitempty"`
	Errors []string       `json:"errors,omitempty"`
}

type refineStage struct {
	ReviewJSON string         `json:"review_json"`
	RefineJSON string         `json:"refine_json"`
	Model      string         `json:"model"`
	TokensIn   int            `json:"tokens_in"`
	TokensOut  int            `json:"tokens_out"`
	Findings   []eval.Finding `json:"b15_findings,omitempty"`
	Blocks     []refineBlock  `json:"blocks"`
}

type corpusStage struct {
	AcceptedCount int      `json:"accepted_count"`
	BlockIDs      []string `json:"block_ids"`
	// Expressions feed the scene-group topic stage and let the run measure
	// cross-session duplication (86_ M5).
	Expressions []string `json:"expressions"`
}

type check struct {
	Name   string `json:"name"`
	Passed bool   `json:"passed"`
	Detail string `json:"detail,omitempty"`
}

type judgeOutcome struct {
	Target  string `json:"target,omitempty"`
	Answer  string `json:"answer"`
	Expect  string `json:"expect"`
	Got     string `json:"got,omitempty"`
	Correct bool   `json:"correct"`
	Reason  string `json:"reason,omitempty"`
	Skipped string `json:"skipped,omitempty"`
	Error   string `json:"error,omitempty"`
	// DurationMS is the production-budget pass, kept so the report can say how
	// the 1.5s DrillTimeouts behave in practice.
	ProdDurationMS int64 `json:"prod_duration_ms,omitempty"`
	// ProdTimedOut records that the production-budget call hit its deadline —
	// that is a health signal about the budget, not a judgement.
	ProdTimedOut bool `json:"prod_timed_out,omitempty"`
	// JudgeDurationMS is how long the model actually took when given room, which
	// is what tells the budget question apart from the model-speed question.
	JudgeDurationMS int64 `json:"judge_duration_ms,omitempty"`
}

type hitOutcome struct {
	Text    string `json:"text"`
	Expect  string `json:"expect"`
	Kind    string `json:"kind,omitempty"`
	Got     string `json:"got,omitempty"`
	Correct bool   `json:"correct"`
	Error   string `json:"error,omitempty"`
}

type topicOutcome struct {
	Title      string   `json:"title"`
	CardType   string   `json:"card_type"`
	PromptEN   string   `json:"prompt_en"`
	BlockIDs   []string `json:"block_ids"`
	SourceNote string   `json:"source_note"`
}

// --- L1 rule checks -----------------------------------------------------

// ruleChecks are the machine-verifiable half of the standard: everything here is
// a fact about the output, not a judgement about it.
func ruleChecks(s sample, transcript string, blocks []refineBlock, accepted []corpus.PhraseBlockView) []check {
	checks := make([]check, 0, 8)
	add := func(name string, passed bool, detail string) {
		checks = append(checks, check{Name: name, Passed: passed, Detail: detail})
	}

	// Blocks present and countable.
	add("blocks_present", len(blocks) > 0, fmt.Sprintf("%d blocks", len(blocks)))
	inRange := len(blocks) >= s.Expectations.MinBlocks && len(blocks) <= s.Expectations.MaxBlocks
	add("blocks_in_range", inRange, fmt.Sprintf("%d not in [%d,%d]",
		len(blocks), s.Expectations.MinBlocks, s.Expectations.MaxBlocks))

	// Anchors: the transcript is the only evidence that a stuck point was real
	// (PRD §5.2.1), so an anchor that is not in it means the block was invented.
	lowerTranscript := strings.ToLower(transcript)
	anchorOK := len(blocks) > 0
	var badAnchor string
	for _, b := range blocks {
		if !strings.Contains(lowerTranscript, strings.ToLower(strings.TrimSpace(b.AnchorUserSaid))) {
			anchorOK = false
			badAnchor = b.AnchorUserSaid
			break
		}
	}
	add("anchors_in_transcript", anchorOK, "anchor not found: "+badAnchor)

	// Tags must be from the closed enums, or the corpus filters break.
	tagsOK := true
	var badTag string
	for _, b := range blocks {
		if _, ok := eval.SceneTags[b.SceneTag]; !ok {
			tagsOK, badTag = false, "scene="+b.SceneTag
			break
		}
		if _, ok := eval.FunctionTags[b.FunctionTag]; !ok {
			tagsOK, badTag = false, "function="+b.FunctionTag
			break
		}
	}
	add("tags_valid", tagsOK, badTag)

	// Topic coverage: the refined sentence should touch the subject the learner
	// was actually talking about.
	topicHit := false
	for _, b := range blocks {
		lower := strings.ToLower(b.ExpressionEN)
		for _, t := range s.Expectations.Topics {
			if strings.Contains(lower, strings.ToLower(t)) {
				topicHit = true
			}
		}
	}
	add("topics_covered", topicHit, "expected one of "+strings.Join(s.Expectations.Topics, "/"))

	// Duplicates dilute the corpus (86_ M5): same meaning twice is not an asset.
	normalized := map[string]struct{}{}
	dup := ""
	for _, b := range blocks {
		key := normalizeExpression(b.ExpressionEN)
		if _, ok := normalized[key]; ok {
			dup = b.ExpressionEN
			break
		}
		normalized[key] = struct{}{}
	}
	add("no_duplicate_blocks", dup == "", "duplicate: "+dup)

	// The rescue anchor is the stuck point the product promised to turn into an
	// asset (PRD §5.2.2): when the sample reported one, some block should carry it.
	if len(s.RescueEvents) > 0 {
		expectedAnchor := ""
		for _, e := range s.RescueEvents {
			if strings.TrimSpace(e.Anchor) != "" {
				expectedAnchor = e.Anchor
			}
		}
		if expectedAnchor != "" {
			used := false
			for _, b := range blocks {
				if strings.Contains(strings.ToLower(b.AnchorUserSaid), strings.ToLower(expectedAnchor)) {
					used = true
					break
				}
			}
			add("rescue_anchor_used", used, "expected anchor: "+expectedAnchor)
		}
	}

	add("corpus_accepted", len(accepted) == len(blocks), fmt.Sprintf("%d accepted of %d", len(accepted), len(blocks)))
	return checks
}

func normalizeExpression(s string) string {
	lower := strings.ToLower(strings.TrimSpace(s))
	lower = strings.TrimRight(lower, ".!?")
	replacer := strings.NewReplacer(",", "", ";", "", "  ", " ")
	return strings.TrimSpace(replacer.Replace(lower))
}

// --- aggregation --------------------------------------------------------

type report struct {
	RunID     string    `json:"run_id"`
	StartedAt time.Time `json:"started_at"`
	ElapsedMS int64     `json:"elapsed_ms"`
	Samples   int       `json:"samples"`
	Errors    int       `json:"samples_with_errors"`
	// Valid is false when too much of the run failed to run at all. Metrics
	// computed over mostly-failed samples are not quality numbers — an overdue
	// vendor account once rendered as "quality: 0.000" across the board, which
	// reads as a catastrophic product rather than a broken run.
	Valid         bool      `json:"valid"`
	InvalidReason string    `json:"invalid_reason,omitempty"`
	FirstErrors   []string  `json:"first_errors,omitempty"`
	Metrics       []metric  `json:"metrics"`
	Gate          gate      `json:"gate"`
	Cost          costBlock `json:"eval_cost"`
}

// invalidRunErrorShare is the share of samples that may fail on infrastructure
// before the run stops calling itself a measurement.
const invalidRunErrorShare = 0.2

type metric struct {
	Name   string  `json:"name"`
	Value  float64 `json:"value"`
	Target string  `json:"target"`
	Passed bool    `json:"passed"`
	Detail string  `json:"detail,omitempty"`
}

type gate struct {
	Passed  bool     `json:"passed"`
	Reasons []string `json:"reasons,omitempty"`
}

type costBlock struct {
	Calls     int   `json:"calls"`
	TokensIn  int   `json:"tokens_in"`
	TokensOut int   `json:"tokens_out"`
	MicroYuan int64 `json:"cost_micro_yuan"`
	// ByTaskType is the reconciliation view: what the ledger recorded per
	// operation, so the rows can be checked against the calls the run made.
	ByTaskType []costRow `json:"by_task_type"`
	// ByModel proves which models the ledger attributed the calls to — the
	// endpoint→model mapping is easy to get wrong and silent when it is.
	ByModel []costRow `json:"by_model"`
}

type costRow struct {
	Key       string `json:"key"`
	Calls     int    `json:"calls"`
	TokensIn  int    `json:"tokens_in"`
	TokensOut int    `json:"tokens_out"`
	MicroYuan int64  `json:"cost_micro_yuan"`
}

func aggregate(runID string, results []*sampleResult, topics topicStageResult, elapsed time.Duration, costSvc *aicost.Service) report {
	rep := report{RunID: runID, StartedAt: time.Now().UTC().Add(-elapsed), ElapsedMS: elapsed.Milliseconds(), Samples: len(results)}

	// L1 counters.
	var structOK, anchorOK, countOK, tagsOK, dupOK, topicsOK, rescueOK int
	var rescueSamples int
	for _, r := range results {
		if len(r.Errors) > 0 {
			rep.Errors++
		}
		for _, c := range r.Checks {
			if c.Passed {
				switch c.Name {
				case "anchors_in_transcript":
					anchorOK++
				case "blocks_in_range":
					countOK++
				case "tags_valid":
					tagsOK++
				case "no_duplicate_blocks":
					dupOK++
				case "topics_covered":
					topicsOK++
				case "rescue_anchor_used":
					rescueOK++
				}
			}
			if c.Name == "rescue_anchor_used" {
				rescueSamples++
			}
		}
		if len(r.Refine.Findings) == 0 {
			structOK++
		}
	}
	n := float64(len(results))
	rep.Metrics = append(rep.Metrics,
		ratio("b15_schema_valid_rate", structOK, n, "= 1.0"),
		ratio("anchors_in_transcript_rate", anchorOK, n, "= 1.0"),
		ratio("blocks_in_range_rate", countOK, n, ">= 0.95"),
		ratio("tags_valid_rate", tagsOK, n, "= 1.0"),
		ratio("no_duplicate_blocks_rate", dupOK, n, ">= 0.95"),
		ratio("topics_covered_rate", topicsOK, n, ">= 0.95"),
	)
	if rescueSamples > 0 {
		rep.Metrics = append(rep.Metrics, ratio("rescue_anchor_used_rate", rescueOK, float64(rescueSamples), ">= 0.80"))
	}

	// Judge accuracy (M1's gate) and the production budget's health, kept apart:
	// one says the prompt judges well, the other says whether the realtime path
	// can get an answer at all.
	var judgeTotal, judgeCorrect, judgeSkipped, judgeErrors int
	var prodCalls, prodTimeouts int
	var prodDurationSum int64
	for _, r := range results {
		for _, j := range r.Judge {
			if j.ProdDurationMS > 0 || j.ProdTimedOut {
				prodCalls++
				prodDurationSum += j.ProdDurationMS
				if j.ProdTimedOut {
					prodTimeouts++
				}
			}
			switch {
			case j.Skipped != "":
				judgeSkipped++
			case j.Error != "":
				judgeErrors++
			default:
				judgeTotal++
				if j.Correct {
					judgeCorrect++
				}
			}
		}
	}
	judgeMetric := ratio("drill_judge_accuracy", judgeCorrect, float64(judgeTotal), ">= 0.95")
	judgeMetric.Detail = fmt.Sprintf("%d/%d scored, %d skipped, %d errors", judgeCorrect, judgeTotal, judgeSkipped, judgeErrors)
	rep.Metrics = append(rep.Metrics, judgeMetric)

	if prodCalls > 0 {
		timeoutMetric := ratio("drill_judge_prod_timeout_rate", prodTimeouts, float64(prodCalls), "<= 0.10")
		timeoutMetric.Detail = fmt.Sprintf("production budget %s, mean %.0fms over %d calls",
			drill.JudgeTimeout, float64(prodDurationSum)/float64(prodCalls), prodCalls)
		rep.Metrics = append(rep.Metrics, timeoutMetric)
	}

	// Hit detection. The gated pair is (own expression hits, unrelated sentence
	// misses); the paraphrase case is reported as recall because the threshold
	// is deliberately conservative.
	var hitGated, hitFP, hitTN, hitTP, hitFN int
	var paraTotal, paraHit int
	for _, r := range results {
		for _, h := range r.Hit {
			if h.Error != "" {
				continue
			}
			if h.Kind == "paraphrase" {
				paraTotal++
				if h.Got == "hit" {
					paraHit++
				}
				continue
			}
			hitGated++
			switch {
			case h.Expect == "miss" && h.Got == "hit":
				hitFP++
			case h.Expect == "miss":
				hitTN++
			case h.Expect == "hit" && h.Got == "hit":
				hitTP++
			default:
				hitFN++
			}
		}
	}
	rep.Metrics = append(rep.Metrics,
		ratio("hit_false_positive_rate", hitFP, float64(hitFP+hitTN), "<= 0.05"),
		ratio("hit_self_hit_rate", hitTP, float64(hitTP+hitFN), ">= 0.95"),
	)
	if paraTotal > 0 {
		recall := ratio("hit_paraphrase_recall", paraHit, float64(paraTotal), "informational")
		recall.Passed = true
		recall.Detail = "the detector trades recall for precision on purpose (threshold 0.65)"
		rep.Metrics = append(rep.Metrics, recall)
	}

	// L2 rubric aggregates.
	var fidelitySum, idiomaticSum, portableSum, blockScores int
	var lowFidelity int
	var authenticitySum, authenticityCount int
	for _, r := range results {
		if r.Scores == nil {
			continue
		}
		for _, b := range r.Scores.Blocks {
			blockScores++
			fidelitySum += b.Fidelity
			idiomaticSum += b.Idiomatic
			portableSum += b.Portable
			if b.Fidelity > 0 && b.Fidelity < 4 {
				lowFidelity++
			}
		}
		if r.Scores.Review.Authenticity > 0 {
			authenticitySum += r.Scores.Review.Authenticity
			authenticityCount++
		}
	}
	if blockScores > 0 {
		rep.Metrics = append(rep.Metrics,
			mean("refine_fidelity_mean", fidelitySum, blockScores, ">= 4.0"),
			mean("refine_idiomatic_mean", idiomaticSum, blockScores, ">= 4.0"),
			mean("refine_portable_mean", portableSum, blockScores, ">= 4.0"),
			ratio("refine_low_fidelity_rate", lowFidelity, float64(blockScores), "<= 0.05"),
		)
	}
	if authenticityCount > 0 {
		rep.Metrics = append(rep.Metrics, mean("review_authenticity_mean", authenticitySum, authenticityCount, ">= 3.8"))
	}
	// Topic cards: structural expectations (H2's hard constraints) plus the
	// referee's groundedness read, over the pooled corpus.
	var withBlocks, withNote, cards, corpusSize, duplicates, sessions int
	var groundedSum, topicScores, genericCount int
	for _, stat := range topics.SceneDuplicates {
		corpusSize += stat.Blocks
		duplicates += stat.Duplicates
		sessions += stat.Sessions
	}
	for _, t := range topics.Cards {
		cards++
		if len(t.BlockIDs) > 0 {
			withBlocks++
		}
		if strings.TrimSpace(t.SourceNote) != "" {
			withNote++
		}
	}
	if topics.Verdict != nil {
		for _, t := range topics.Verdict.Topics {
			topicScores++
			groundedSum += t.Grounded
			if t.Generic {
				genericCount++
			}
		}
	}
	produced := metric{
		Name: "topic_cards_produced", Value: float64(cards), Target: "> 0", Passed: cards > 0,
		Detail: fmt.Sprintf("pooled corpus %d blocks from %d sessions over %d days", topics.CorpusSize, sessions, topics.Days),
	}
	if topics.Error != "" {
		produced.Passed = false
		produced.Detail = produced.Detail + " | " + topics.Error
	}
	rep.Metrics = append(rep.Metrics, produced)
	if cards > 0 {
		rep.Metrics = append(rep.Metrics,
			ratio("topic_cards_with_blocks_rate", withBlocks, float64(cards), "= 1.0"),
			ratio("topic_cards_with_source_rate", withNote, float64(cards), "= 1.0"),
		)
	}
	if topicScores > 0 {
		rep.Metrics = append(rep.Metrics,
			mean("topic_grounded_mean", groundedSum, topicScores, ">= 4.0"),
			ratio("topic_generic_rate", genericCount, float64(topicScores), "<= 0.05"),
		)
	}
	// M5: how often two sessions produced the same expression.
	if corpusSize > 0 {
		rep.Metrics = append(rep.Metrics, ratio("corpus_cross_session_duplicate_rate", duplicates, float64(corpusSize), "<= 0.10"))
	}

	// A run where most samples could not execute is not a measurement: say that
	// instead of scoring the wreckage. (An overdue vendor account once rendered
	// as "quality: 0.000" across the board, which reads as a catastrophic
	// product rather than a broken run.)
	if n := float64(len(results)); n > 0 && float64(rep.Errors)/n > invalidRunErrorShare {
		rep.Valid = false
		rep.InvalidReason = fmt.Sprintf("%d/%d samples failed before the chain ran (threshold %.0f%%)",
			rep.Errors, len(results), invalidRunErrorShare*100)
	} else {
		rep.Valid = true
	}
	for _, r := range results {
		if len(r.Errors) > 0 && len(rep.FirstErrors) < 3 {
			rep.FirstErrors = append(rep.FirstErrors, r.Sample.ID+": "+r.Errors[0])
		}
	}

	// The gate: hard constraints, plus the judge accuracy that everything else
	// rests on (86_ M1).
	rep.Gate = evaluateGate(rep.Metrics)

	// The eval's own cost, from the ledger every call wrote to.
	if costSvc != nil {
		if logs, err := costSvc.ListRecent(contextTODO(), "", 10000); err == nil {
			byTask := map[string]*costRow{}
			byModel := map[string]*costRow{}
			taskOrder := make([]string, 0, 8)
			modelOrder := make([]string, 0, 8)
			for _, log := range logs {
				rep.Cost.Calls++
				rep.Cost.TokensIn += log.TokensIn
				rep.Cost.TokensOut += log.TokensOut
				rep.Cost.MicroYuan += log.CostMicroYuan
				task, ok := byTask[log.TaskType]
				if !ok {
					task = &costRow{Key: log.TaskType}
					byTask[log.TaskType] = task
					taskOrder = append(taskOrder, log.TaskType)
				}
				model, ok := byModel[log.Model]
				if !ok {
					model = &costRow{Key: log.Model}
					byModel[log.Model] = model
					modelOrder = append(modelOrder, log.Model)
				}
				for _, row := range []*costRow{task, model} {
					row.Calls++
					row.TokensIn += log.TokensIn
					row.TokensOut += log.TokensOut
					row.MicroYuan += log.CostMicroYuan
				}
			}
			sort.Strings(taskOrder)
			sort.Strings(modelOrder)
			for _, key := range taskOrder {
				rep.Cost.ByTaskType = append(rep.Cost.ByTaskType, *byTask[key])
			}
			for _, key := range modelOrder {
				rep.Cost.ByModel = append(rep.Cost.ByModel, *byModel[key])
			}
		}
	}
	return rep
}

func ratio(name string, hit int, total float64, target string) metric {
	value := 0.0
	if total > 0 {
		value = float64(hit) / total
	}
	return metric{Name: name, Value: value, Target: target, Passed: meetsTarget(value, target)}
}

func mean(name string, sum, count int, target string) metric {
	value := 0.0
	if count > 0 {
		value = float64(sum) / float64(count)
	}
	return metric{Name: name, Value: value, Target: target, Passed: meetsTarget(value, target)}
}

// meetsTarget understands the two shapes the table uses: ">= X", "<= X", "= X".
func meetsTarget(value float64, target string) bool {
	target = strings.TrimSpace(target)
	switch {
	case strings.HasPrefix(target, ">="):
		return value >= parseFloat(strings.TrimSpace(strings.TrimPrefix(target, ">=")))
	case strings.HasPrefix(target, "<="):
		return value <= parseFloat(strings.TrimSpace(strings.TrimPrefix(target, "<=")))
	case strings.HasPrefix(target, "= "):
		return value >= parseFloat(strings.TrimSpace(strings.TrimPrefix(target, "= ")))-1e-9
	case strings.HasPrefix(target, "> "):
		return value > parseFloat(strings.TrimSpace(strings.TrimPrefix(target, ">")))
	default:
		return true
	}
}

func parseFloat(s string) float64 {
	var f float64
	_, _ = fmt.Sscanf(s, "%f", &f)
	return f
}

// evaluateGate turns the metric table into a ship/no-ship decision. Only the
// hard constraints block: a quality mean slightly under target is a
// "investigate", not a "stop the release".
func evaluateGate(metrics []metric) gate {
	blocking := map[string]bool{
		"b15_schema_valid_rate":        true,
		"anchors_in_transcript_rate":   true,
		"tags_valid_rate":              true,
		"drill_judge_accuracy":         true,
		"topic_cards_produced":         true,
		"topic_cards_with_blocks_rate": true,
		"topic_cards_with_source_rate": true,
	}
	out := gate{Passed: true}
	for _, m := range metrics {
		if !m.Passed && blocking[m.Name] {
			out.Passed = false
			out.Reasons = append(out.Reasons, fmt.Sprintf("%s = %.3f (target %s)", m.Name, m.Value, m.Target))
		}
	}
	return out
}

// --- artifacts ----------------------------------------------------------

func writeArtifacts(outDir string, results []*sampleResult, topics topicStageResult, rep report) error {
	for _, r := range results {
		raw, err := json.MarshalIndent(r, "", "  ")
		if err != nil {
			return err
		}
		if err := os.WriteFile(filepath.Join(outDir, "samples", r.Sample.ID+".json"), append(raw, '\n'), 0o644); err != nil {
			return err
		}
	}
	topicRaw, err := json.MarshalIndent(topics, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(outDir, "topics.json"), append(topicRaw, '\n'), 0o644); err != nil {
		return err
	}
	raw, err := json.MarshalIndent(rep, "", "  ")
	if err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(outDir, "report.json"), append(raw, '\n'), 0o644); err != nil {
		return err
	}
	if err := os.WriteFile(filepath.Join(outDir, "report.md"), []byte(renderMarkdown(rep, results, topics)), 0o644); err != nil {
		return err
	}
	return os.WriteFile(filepath.Join(outDir, "review-sheet.md"), []byte(renderReviewSheet(results)), 0o644)
}

func renderMarkdown(rep report, results []*sampleResult, topics topicStageResult) string {
	var b strings.Builder
	fmt.Fprintf(&b, "# 核心链路质量评估报告 %s\n\n", rep.RunID)
	fmt.Fprintf(&b, "- 样本数：%d（%d 条有错误）\n- 耗时：%.1fs\n- 评估自身成本：%d 次调用 / %d+%d tokens / %.4f 元\n\n",
		rep.Samples, rep.Errors, float64(rep.ElapsedMS)/1000.0,
		rep.Cost.Calls, rep.Cost.TokensIn, rep.Cost.TokensOut, float64(rep.Cost.MicroYuan)/1e6)

	if !rep.Valid {
		fmt.Fprintf(&b, "## ⚠️ 本次运行无效\n\n%s\n\n前若干错误：\n", rep.InvalidReason)
		for _, e := range rep.FirstErrors {
			fmt.Fprintf(&b, "- %s\n", e)
		}
		b.WriteString("\n（以下指标建在失败的样本上，**不可当作质量结论**。）\n\n")
	}
	if rep.Gate.Passed {
		b.WriteString("## 总门禁：✅ 通过\n\n")
	} else {
		b.WriteString("## 总门禁：❌ 未通过\n\n")
		for _, reason := range rep.Gate.Reasons {
			fmt.Fprintf(&b, "- %s\n", reason)
		}
		b.WriteString("\n")
	}

	b.WriteString("## 指标\n\n| 指标 | 实测 | 门槛 | 结果 | 备注 |\n|---|---|---|---|---|\n")
	for _, m := range rep.Metrics {
		mark := "✅"
		if !m.Passed {
			mark = "❌"
		}
		fmt.Fprintf(&b, "| %s | %.3f | %s | %s | %s |\n", m.Name, m.Value, m.Target, mark, m.Detail)
	}

	b.WriteString("\n## 失败样本（规则层）\n\n")
	found := false
	for _, r := range results {
		for _, c := range r.Checks {
			if !c.Passed {
				found = true
				fmt.Fprintf(&b, "- `%s` [%s] %s: %s\n", r.Sample.ID, r.Sample.Scene, c.Name, c.Detail)
			}
		}
	}
	if !found {
		b.WriteString("（无）\n")
	}

	b.WriteString("\n## 判定错误（L2 金标对照）\n\n")
	found = false
	for _, r := range results {
		for _, j := range r.Judge {
			if j.Skipped == "" && j.Error == "" && !j.Correct {
				found = true
				fmt.Fprintf(&b, "- `%s` expect=%s got=%s\n  - target: %s\n  - answer: %s\n  - reason: %s\n",
					r.Sample.ID, j.Expect, j.Got, j.Target, j.Answer, j.Reason)
			}
		}
	}
	if !found {
		b.WriteString("（无）\n")
	}

	b.WriteString("\n## 记账核对（本次运行产生的 ai_cost_logs 行）\n\n")
	b.WriteString("| task_type | 调用 | tokens_in | tokens_out | 微元 |\n|---|---|---|---|---|\n")
	for _, row := range rep.Cost.ByTaskType {
		fmt.Fprintf(&b, "| %s | %d | %d | %d | %d |\n", row.Key, row.Calls, row.TokensIn, row.TokensOut, row.MicroYuan)
	}
	b.WriteString("\n| model | 调用 | tokens_in | tokens_out | 微元 |\n|---|---|---|---|---|\n")
	for _, row := range rep.Cost.ByModel {
		fmt.Fprintf(&b, "| %s | %d | %d | %d | %d |\n", row.Key, row.Calls, row.TokensIn, row.TokensOut, row.MicroYuan)
	}

	b.WriteString("\n## 话题阶段\n\n")
	fmt.Fprintf(&b, "池化语料 %d 块（%d 天，%d 张卡）%s\n\n", topics.CorpusSize, topics.Days, len(topics.Cards), topics.Error)
	b.WriteString("| 场景 | 会话数 | 块数 | 跨会话重复 |\n|---|---|---|---|\n")
	for _, stat := range topics.SceneDuplicates {
		fmt.Fprintf(&b, "| %s | %d | %d | %d |\n", stat.Scene, stat.Sessions, stat.Blocks, stat.Duplicates)
	}

	b.WriteString("\n## 质量长尾（裁判低分项）\n\n")
	found = false
	for _, r := range results {
		if r.Scores == nil {
			continue
		}
		for _, blk := range r.Scores.Blocks {
			if blk.Fidelity < 4 || blk.Idiomatic < 4 || blk.Portable < 4 {
				found = true
				expr := ""
				if blk.Index >= 0 && blk.Index < len(r.Refine.Blocks) {
					expr = r.Refine.Blocks[blk.Index].ExpressionEN
				}
				fmt.Fprintf(&b, "- `%s` fid=%d idio=%d port=%d %s\n  - %s\n  - %s\n",
					r.Sample.ID, blk.Fidelity, blk.Idiomatic, blk.Portable, expr, blk.Comment, r.Sample.Scene)
			}
		}
	}
	if !found {
		b.WriteString("（无）\n")
	}
	return b.String()
}

// renderReviewSheet lists what a human should look at: the disagreements (L2
// vs gold labels) first, then a fixed sample so the referee itself gets audited.
func renderReviewSheet(results []*sampleResult) string {
	var b strings.Builder
	b.WriteString("# 人工抽检清单\n\n")
	b.WriteString("先看争议项（判定与金标不一致），再看固定抽样（校准裁判本身）。\n\n")

	b.WriteString("## 一、判定争议\n\n")
	count := 0
	for _, r := range results {
		for _, j := range r.Judge {
			if j.Skipped == "" && j.Error == "" && !j.Correct {
				count++
				fmt.Fprintf(&b, "### %s（expect=%s, got=%s）\n\n- 目标句：%s\n- 学员答：%s\n- 判定理由：%s\n- 你的判断：pass / fail\n\n",
					r.Sample.ID, j.Expect, j.Got, j.Target, j.Answer, j.Reason)
			}
		}
	}
	if count == 0 {
		b.WriteString("（无）\n\n")
	}

	b.WriteString("## 二、固定抽样（每 10 条取 1）\n\n")
	for i, r := range results {
		if i%10 != 0 {
			continue
		}
		fmt.Fprintf(&b, "### %s（%s / %s）\n\n", r.Sample.ID, r.Sample.Scene, r.Sample.Profile)
		for _, blk := range r.Refine.Blocks {
			fmt.Fprintf(&b, "- 意图：%s\n- 表达：%s\n- 锚点：%s\n\n", blk.IntentZH, blk.ExpressionEN, blk.AnchorUserSaid)
		}
		if r.Scores != nil {
			for _, s := range r.Scores.Blocks {
				fmt.Fprintf(&b, "- 裁判：fid=%d idio=%d port=%d（%s）\n", s.Fidelity, s.Idiomatic, s.Portable, s.Comment)
			}
			fmt.Fprintf(&b, "- 评价真实性：%d（%s）\n", r.Scores.Review.Authenticity, r.Scores.Review.Comment)
		}
		b.WriteString("\n")
	}
	return b.String()
}

func printSummary(rep report, outDir string) {
	fmt.Printf("\n=== 核心链路质量评估 %s ===\n", rep.RunID)
	if !rep.Valid {
		fmt.Printf("  ⚠️  本次运行无效：%s\n", rep.InvalidReason)
		for _, e := range rep.FirstErrors {
			fmt.Printf("      %s\n", e)
		}
		fmt.Printf("  报告：%s/report.md\n", outDir)
		return
	}
	for _, m := range rep.Metrics {
		mark := "PASS"
		if !m.Passed {
			mark = "FAIL"
		}
		fmt.Printf("  [%-4s] %-32s %.3f  (target %s)\n", mark, m.Name, m.Value, m.Target)
	}
	if rep.Gate.Passed {
		fmt.Printf("\n总门禁：通过。报告：%s/report.md\n", outDir)
	} else {
		fmt.Printf("\n总门禁：未通过 —— %s\n报告：%s/report.md\n", strings.Join(rep.Gate.Reasons, "; "), outDir)
	}
}

func contextTODO() context.Context { return context.Background() }

var _ = sort.Strings
