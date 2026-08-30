package eval

import (
	"encoding/json"
	"testing"
)

func TestValidateSamplePass(t *testing.T) {
	s := Sample{
		ID:         "s1",
		Transcript: "I will sync up with the team tomorrow about the cache issue.",
		Review: json.RawMessage(`{
			"goal_achievement": {"met": true, "note": "ok"},
			"issues": [{"type":"idiomatic","original_quote":"sync up with the team"}],
			"suggestions": [{"text":"prefer touch base"}],
			"comparisons": [{"a":1},{"b":2},{"c":3}]
		}`),
		Refine: json.RawMessage(`{
			"blocks":[{
				"intent_zh":"同步进度",
				"expression_en":"I'll touch base with the team tomorrow.",
				"anchor_user_said":"sync up with the team",
				"scene_tag":"standup",
				"function_tag":"report"
			}]
		}`),
	}
	if fs := ValidateSample(s); len(fs) != 0 {
		t.Fatalf("unexpected findings: %+v", fs)
	}
}

func TestValidateSampleQuoteMustBeInTranscript(t *testing.T) {
	s := Sample{
		ID:         "s2",
		Transcript: "hello world",
		Review: json.RawMessage(`{
			"goal_achievement": {},
			"issues":[{"type":"grammar","original_quote":"not present"}],
			"suggestions":[],
			"comparisons":[{},{},{}]
		}`),
		Refine: json.RawMessage(`{
			"blocks":[{
				"intent_zh":"打招呼",
				"expression_en":"Hello.",
				"anchor_user_said":"hello",
				"scene_tag":"casual",
				"function_tag":"ask"
			}]
		}`),
	}
	fs := ValidateSample(s)
	if len(fs) == 0 {
		t.Fatal("expected quote finding")
	}
}

func TestRunDataset(t *testing.T) {
	samples := []Sample{
		{
			ID:         "ok",
			Transcript: "We should defer the discussion.",
			Review:     json.RawMessage(`{"goal_achievement":{},"issues":[],"suggestions":[],"comparisons":[{},{},{}]}`),
			Refine: json.RawMessage(`{"blocks":[{
				"intent_zh":"推迟讨论",
				"expression_en":"Let's table this for now.",
				"anchor_user_said":"defer the discussion",
				"scene_tag":"review",
				"function_tag":"defer"
			}]}`),
		},
		{
			ID:         "bad-tag",
			Transcript: "We should defer the discussion.",
			Review:     json.RawMessage(`{"goal_achievement":{},"issues":[],"suggestions":[],"comparisons":[{},{},{}]}`),
			Refine: json.RawMessage(`{"blocks":[{
				"intent_zh":"推迟讨论",
				"expression_en":"Let's table this for now.",
				"anchor_user_said":"defer the discussion",
				"scene_tag":"unknown-scene",
				"function_tag":"defer"
			}]}`),
		},
	}
	res := RunDataset(samples)
	if res.Passed != 1 || res.Failed != 1 {
		t.Fatalf("result = %+v", res)
	}
}
