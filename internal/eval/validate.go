// Package eval implements B15 offline prompt regression checks for review/refine JSON.
package eval

import (
	"encoding/json"
	"fmt"
	"strings"
)

// SceneTags and FunctionTags are the closed enums from prompt design doc §3.3.
var (
	SceneTags = map[string]struct{}{
		"standup": {}, "review": {}, "1on1": {}, "interview": {}, "casual": {},
	}
	FunctionTags = map[string]struct{}{
		"object": {}, "clarify": {}, "report": {}, "propose": {}, "agree": {},
		"disagree": {}, "ask": {}, "summarize": {}, "defer": {}, "commit": {},
	}
	IssueTypes = map[string]struct{}{
		"grammar": {}, "idiomatic": {}, "missing_info": {},
	}
)

// Sample is one offline evaluation case.
type Sample struct {
	ID         string          `json:"id"`
	Transcript string          `json:"transcript"`
	Review     json.RawMessage `json:"review"`
	Refine     json.RawMessage `json:"refine"`
}

// Finding is one regression failure.
type Finding struct {
	SampleID string `json:"sample_id"`
	Rule     string `json:"rule"`
	Detail   string `json:"detail"`
}

// Result aggregates findings for a sample set.
type Result struct {
	Samples  int       `json:"samples"`
	Passed   int       `json:"passed"`
	Failed   int       `json:"failed"`
	Findings []Finding `json:"findings,omitempty"`
}

type reviewDoc struct {
	GoalAchievement json.RawMessage `json:"goal_achievement"`
	Issues          []reviewIssue   `json:"issues"`
	Suggestions     []any           `json:"suggestions"`
	Comparisons     []any           `json:"comparisons"`
}

type reviewIssue struct {
	Type          string `json:"type"`
	OriginalQuote string `json:"original_quote"`
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

// ValidateSample checks schema legality, quote-in-transcript, and refine triple completeness.
func ValidateSample(s Sample) []Finding {
	var findings []Finding
	if strings.TrimSpace(s.ID) == "" {
		findings = append(findings, Finding{Rule: "sample.id", Detail: "id is required"})
		return findings
	}
	if strings.TrimSpace(s.Transcript) == "" {
		findings = append(findings, Finding{SampleID: s.ID, Rule: "sample.transcript", Detail: "transcript is required"})
	}

	var review reviewDoc
	if err := json.Unmarshal(s.Review, &review); err != nil {
		findings = append(findings, Finding{SampleID: s.ID, Rule: "review.schema", Detail: err.Error()})
	} else {
		findings = append(findings, validateReview(s.ID, s.Transcript, review)...)
	}

	var refine refineDoc
	if err := json.Unmarshal(s.Refine, &refine); err != nil {
		findings = append(findings, Finding{SampleID: s.ID, Rule: "refine.schema", Detail: err.Error()})
	} else {
		findings = append(findings, validateRefine(s.ID, s.Transcript, refine)...)
	}
	return findings
}

// RunDataset validates every sample and returns an aggregate result.
func RunDataset(samples []Sample) Result {
	res := Result{Samples: len(samples)}
	for _, s := range samples {
		fs := ValidateSample(s)
		if len(fs) == 0 {
			res.Passed++
			continue
		}
		res.Failed++
		res.Findings = append(res.Findings, fs...)
	}
	return res
}

func validateReview(id, transcript string, doc reviewDoc) []Finding {
	var findings []Finding
	if len(doc.Issues) > 5 {
		findings = append(findings, Finding{SampleID: id, Rule: "review.issues.max", Detail: fmt.Sprintf("got %d want <=5", len(doc.Issues))})
	}
	if len(doc.Suggestions) > 3 {
		findings = append(findings, Finding{SampleID: id, Rule: "review.suggestions.max", Detail: fmt.Sprintf("got %d want <=3", len(doc.Suggestions))})
	}
	if n := len(doc.Comparisons); n < 3 || n > 8 {
		findings = append(findings, Finding{SampleID: id, Rule: "review.comparisons.range", Detail: fmt.Sprintf("got %d want 3-8", n)})
	}
	for i, issue := range doc.Issues {
		if _, ok := IssueTypes[issue.Type]; !ok {
			findings = append(findings, Finding{SampleID: id, Rule: "review.issue.type", Detail: fmt.Sprintf("issues[%d].type=%q", i, issue.Type)})
		}
		quote := strings.TrimSpace(issue.OriginalQuote)
		if quote == "" {
			findings = append(findings, Finding{SampleID: id, Rule: "review.issue.quote", Detail: fmt.Sprintf("issues[%d] missing original_quote", i)})
			continue
		}
		if !strings.Contains(transcript, quote) {
			findings = append(findings, Finding{SampleID: id, Rule: "review.issue.quote_in_transcript", Detail: fmt.Sprintf("issues[%d] quote not in transcript: %q", i, quote)})
		}
	}
	return findings
}

func validateRefine(id, transcript string, doc refineDoc) []Finding {
	var findings []Finding
	if len(doc.Blocks) == 0 {
		findings = append(findings, Finding{SampleID: id, Rule: "refine.blocks", Detail: "blocks must be non-empty"})
		return findings
	}
	for i, b := range doc.Blocks {
		prefix := fmt.Sprintf("blocks[%d]", i)
		if strings.TrimSpace(b.IntentZH) == "" || strings.TrimSpace(b.ExpressionEN) == "" || strings.TrimSpace(b.AnchorUserSaid) == "" {
			findings = append(findings, Finding{SampleID: id, Rule: "refine.triple", Detail: prefix + " missing intent_zh/expression_en/anchor_user_said"})
		}
		if !strings.Contains(transcript, strings.TrimSpace(b.AnchorUserSaid)) {
			findings = append(findings, Finding{SampleID: id, Rule: "refine.anchor_in_transcript", Detail: fmt.Sprintf("%s anchor not in transcript: %q", prefix, b.AnchorUserSaid)})
		}
		if _, ok := SceneTags[b.SceneTag]; !ok {
			findings = append(findings, Finding{SampleID: id, Rule: "refine.scene_tag", Detail: fmt.Sprintf("%s scene_tag=%q", prefix, b.SceneTag)})
		}
		if _, ok := FunctionTags[b.FunctionTag]; !ok {
			findings = append(findings, Finding{SampleID: id, Rule: "refine.function_tag", Detail: fmt.Sprintf("%s function_tag=%q", prefix, b.FunctionTag)})
		}
	}
	return findings
}
