package session

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/reviewgen"
)

// D1 在这一层的端到端判据：**两份产出必须同时是重排后的那一份**。
//
// `reviewgen.StuckFirstBlocks` 自己的判据只管那个函数；而这里是产出真正成形的地方，
// 而且有**两个副本** —— `buildReviewPayload` 把 refine 嵌进 review JSON，`RefineJSON`
// 是另一份。给它们传不同的值，同一份产出就会存在两种顺序，而 UI 读哪一份是实现细节。
// 也就是说：这个 bug 会随调用方而变，只有在这里钉得住。
func TestBuildReviewArtifacts_ReordersStuckBlocksInBothCopies(t *testing.T) {
	svc, store := newRescueTestService(t)
	gen := &fakeReviewGenerator{result: reviewgenResultWithBlocks(
		`{"intent_zh":"甲","expression_en":"plain one","anchor_user_said":"plain one"}`,
		`{"intent_zh":"乙","expression_en":"The deploy is blocked.","anchor_user_said":"the deploy is"}`,
	)}
	svc.SetReviewGenerator(gen)

	created, err := svc.Create(context.Background(), "u1", CreateRequest{SceneType: "standup"})
	if err != nil {
		t.Fatalf("Create: %v", err)
	}
	session := Session{ID: created.SessionID, UserID: "u1", SceneType: "standup"}
	if _, _, _, err := store.EndSession(context.Background(), session.ID, StatusEnded, 12, []Utterance{
		{ID: "u-1", Seq: 1, Speaker: SpeakerUser, Text: "the deploy is"},
	}, []RescueEvent{
		{
			ID: "r-1", SessionID: session.ID, UserID: "u1", Seq: 1, TurnID: "turn-1", Level: 3,
			Path: RescuePathIncomplete, Ladder: "The deploy is blocked.", UserOpened: true,
			Anchor: "the deploy is",
		},
	}, time.Now(), nil); err != nil {
		t.Fatalf("EndSession: %v", err)
	}

	artifacts, err := svc.buildReviewArtifacts(context.Background(), session, []Utterance{
		{Speaker: SpeakerUser, Text: "the deploy is"},
	})
	if err != nil {
		t.Fatalf("buildReviewArtifacts: %v", err)
	}

	assertStuckFirst(t, "RefineJSON", artifacts.RefineJSON)
	assertStuckFirst(t, "ReviewJSON（嵌的那一份）", artifacts.ReviewJSON)
}

// assertStuckFirst asserts the stuck block's anchor appears before the plain
// block's, **by position** rather than by parsing: both copies are JSON the UI
// reads, and the requirement is about what it reads, not how it is stored.
func assertStuckFirst(t *testing.T, label string, payload []byte) {
	t.Helper()
	stuck := strings.Index(string(payload), "the deploy is")
	plain := strings.Index(string(payload), "plain one")
	if stuck < 0 || plain < 0 {
		t.Fatalf("%s does not carry both blocks: stuck=%d plain=%d\n%s", label, stuck, plain, payload)
	}
	if stuck > plain {
		t.Fatalf("%s 没把卡壳点来的块排在前面：stuck@%d plain@%d\n%s", label, stuck, plain, payload)
	}
}

// reviewgenResultWithBlocks builds a generator result whose refine carries the
// given blocks in the given order. Spelled here rather than reusing the shared
// fixture so the input is visible at the call site: the whole judgement is about
// order, and an input assembled elsewhere hides it.
func reviewgenResultWithBlocks(blocks ...string) reviewgen.Result {
	return reviewgen.Result{
		Review: json.RawMessage(`{"goal_achievement":{"met":true,"note":"ok"},` +
			`"issues":[],"suggestions":[],"comparisons":[{},{},{}]}`),
		Refine:    json.RawMessage(`{"blocks":[` + strings.Join(blocks, ",") + `]}`),
		Generator: "fake",
	}
}
