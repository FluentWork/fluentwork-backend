package orchestrator

import (
	"testing"

	"github.com/FluentWork/fluentwork-backend/internal/config"
)

// The console configures one deployment per task; before routing, every
// operation called the review endpoint and the other five were dead settings.
func TestEndpointRouting_PerOperation(t *testing.T) {
	cfg := config.Config{
		ArkReviewRefineEP: "ep-review",
		ArkDrillJudgeEP:   "ep-judge",
		ArkTopicCardEP:    "ep-topic",
	}
	client := NewArkClient(cfg, nil)
	t.Cleanup(func() { defaultClient = nil })

	cases := map[string]string{
		"reviewgen.generate": "ep-review",
		"drill.judge":        "ep-judge",
		"topic.generate":     "ep-topic",
		// No route configured and no route defined: the review endpoint carries
		// it, so a new caller never silently loses its model.
		"something.new":    "ep-review",
		"":                 "ep-review",
		"materials.refine": "ep-review", // route defined but endpoint unset → fallback
	}
	for operation, want := range cases {
		if got := client.endpointFor(operation); got != want {
			t.Errorf("endpointFor(%q) = %q, want %q", operation, got, want)
		}
	}

	routing := client.Routing()
	if routing["drill.judge"] != "ep-judge" || routing["topic.generate"] != "ep-topic" {
		t.Fatalf("routing = %+v", routing)
	}
}

// A judge call must not land on the review deployment: that is the whole point
// of the split — the drill judge is short and latency-sensitive.
func TestEndpointRouting_JudgeIsNotTheReviewDeployment(t *testing.T) {
	cfg := config.Config{ArkReviewRefineEP: "ep-review", ArkDrillJudgeEP: "ep-judge"}
	client := NewArkClient(cfg, nil)
	t.Cleanup(func() { defaultClient = nil })

	if client.endpointFor("drill.judge") == client.endpointFor("reviewgen.generate") {
		t.Fatal("judge and review share a deployment; the per-task split is not in effect")
	}
}
