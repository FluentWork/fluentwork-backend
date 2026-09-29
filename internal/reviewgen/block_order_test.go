package reviewgen

import (
	"encoding/json"
	"testing"
)

// anchorsIn returns the blocks' anchors in document order, which is the only
// thing these tests assert on: the requirement is about **order**, not content.
func anchorsIn(t *testing.T, refine json.RawMessage) []string {
	t.Helper()
	var doc struct {
		Blocks []struct {
			AnchorUserSaid string `json:"anchor_user_said"`
		} `json:"blocks"`
	}
	if err := json.Unmarshal(refine, &doc); err != nil {
		t.Fatalf("reordered refine does not parse: %v\n%s", err, refine)
	}
	out := make([]string, 0, len(doc.Blocks))
	for _, block := range doc.Blocks {
		out = append(out, block.AnchorUserSaid)
	}
	return out
}

func equalStrings(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}

// 卡壳点来的块必须排在前面 —— 这是 PRD §7.2 D1 的**要求**，不是建议。
//
// 为什么不能只靠 prompt：模型给的顺序是建议，而这条是产品规则（学员最需要先看到
// 自己卡住的那一句）。卡壳 anchor 与块的 `anchor_user_said` 都在手上，顺序是
// **可推导的**；可推导的事不该去祈祷。
func TestStuckFirstBlocksLeadsWithTheStuckOnes(t *testing.T) {
	refine := json.RawMessage(`{"blocks":[
		{"intent_zh":"甲","anchor_user_said":"plain one"},
		{"intent_zh":"乙","anchor_user_said":"the deploy is"},
		{"intent_zh":"丙","anchor_user_said":"plain two"},
		{"intent_zh":"丁","anchor_user_said":"half sentence"}
	]}`)

	got := StuckFirstBlocks(refine, []string{"the deploy is", "half sentence"})

	want := []string{"the deploy is", "half sentence", "plain one", "plain two"}
	if anchors := anchorsIn(t, got); !equalStrings(anchors, want) {
		t.Fatalf("anchors = %v, want %v — 卡壳点来的块必须排在前面（D1）", anchors, want)
	}
}

// 组内保持原相对顺序：重排是**稳定分区**，不是排序。否则同一批卡壳点之间会凭空
// 换顺序，而那个顺序是模型按重要性给的，我们没有理由覆盖它。
func TestStuckFirstBlocksKeepsTheOrderInsideEachGroup(t *testing.T) {
	refine := json.RawMessage(`{"blocks":[
		{"anchor_user_said":"stuck A"},
		{"anchor_user_said":"plain A"},
		{"anchor_user_said":"stuck B"},
		{"anchor_user_said":"plain B"}
	]}`)

	got := StuckFirstBlocks(refine, []string{"stuck A", "stuck B"})

	want := []string{"stuck A", "stuck B", "plain A", "plain B"}
	if anchors := anchorsIn(t, got); !equalStrings(anchors, want) {
		t.Fatalf("anchors = %v, want %v", anchors, want)
	}
}

// 空白与大小写是唯一的现实漂移，比对要容得下它。
func TestStuckFirstBlocksMatchesAnchorsForgivingly(t *testing.T) {
	refine := json.RawMessage(`{"blocks":[
		{"anchor_user_said":"Plain"},
		{"anchor_user_said":"  the Deploy IS  "}
	]}`)

	got := StuckFirstBlocks(refine, []string{"the deploy is"})

	want := []string{"  the Deploy IS  ", "Plain"}
	if anchors := anchorsIn(t, got); !equalStrings(anchors, want) {
		t.Fatalf("anchors = %v, want %v", anchors, want)
	}
}

// 没有卡壳点可排时，一个字都不该动。
func TestStuckFirstBlocksWithoutStuckAnchorsChangesNothing(t *testing.T) {
	refine := json.RawMessage(`{"blocks":[{"anchor_user_said":"a"},{"anchor_user_said":"b"}]}`)
	if got := string(StuckFirstBlocks(refine, nil)); got != string(refine) {
		t.Fatalf("refine was rewritten with nothing to sort by:\n got %s\nwant %s", got, refine)
	}
}

// 静默路径的卡壳点**没有** anchor（§5.2.2：用户一直没开口 ⇒ 那个事件不产块）。
// 它不能把任何东西拉上来 —— 否则「空匹配空」会把没有来源的块当成卡壳来源。
func TestStuckFirstBlocksIgnoresEmptyStuckAnchors(t *testing.T) {
	refine := json.RawMessage(`{"blocks":[{"anchor_user_said":"a"},{"anchor_user_said":""}]}`)
	if got := string(StuckFirstBlocks(refine, []string{"", "   "})); got != string(refine) {
		t.Fatalf("an empty stuck anchor reordered blocks:\n got %s\nwant %s", got, refine)
	}
}

// 坏输入原样放行：重排是改进，丢块或改写不是。判据钉住「宁可不动」。
func TestStuckFirstBlocksPassesMalformedInputThrough(t *testing.T) {
	for _, bad := range []string{
		`{"blocks": not json}`,
		`{"refine":{}}`,
		``,
	} {
		raw := json.RawMessage(bad)
		if got := string(StuckFirstBlocks(raw, []string{"x"})); got != bad {
			t.Errorf("malformed refine was rewritten:\n got %q\nwant %q", got, bad)
		}
	}
}
