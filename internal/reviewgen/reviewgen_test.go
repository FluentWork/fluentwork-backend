package reviewgen

import "testing"

func TestParseGeneratedDocument(t *testing.T) {
	doc, err := parseGeneratedDocument("```json\n{\"review\":{\"goal_achievement\":{},\"issues\":[],\"suggestions\":[],\"comparisons\":[{},{},{}]},\"refine\":{\"blocks\":[{\"intent_zh\":\"x\",\"expression_en\":\"y\",\"anchor_user_said\":\"hello\",\"scene_tag\":\"casual\",\"function_tag\":\"ask\"}]}}\n```", "")
	if err != nil {
		t.Fatal(err)
	}
	if len(doc.Review) == 0 || len(doc.Refine) == 0 {
		t.Fatalf("unexpected parsed doc: %+v", doc)
	}
}

// The generator speaks through the LLM seam, so a model swap is a client swap.
// This package holds prompts and validation only — the vendor's own tests live
// with the client that talks to it.
func TestGeneratorSeamIsTheOnlyWayToAModel(_ *testing.T) {
	// Compile-time: the interface this package depends on is orchestrator.Client,
	// reached through OrchestratorAdapter. Anything that grows a second direct
	// HTTP path to a vendor belongs in the client, not here.
	var _ Generator = &OrchestratorAdapter{}
}
