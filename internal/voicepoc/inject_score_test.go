package voicepoc

import "testing"

func TestContainsFold(t *testing.T) {
	if !containsFold("请确认 INJECT_OK 缓存", "inject_ok") {
		t.Fatal("marker")
	}
	if !containsFold("你提到了 cache invalidation", "cache") {
		t.Fatal("topic")
	}
	if containsFold("hello", "INJECT_OK") {
		t.Fatal("false positive")
	}
}

func TestScoreInjectReplyRequiresMarker(t *testing.T) {
	got := scoreInjectReply("我看到你提到了 cache invalidation，这个点不错。")
	if got.OK {
		t.Fatalf("scoreInjectReply() should reject topic-only confirmation: %+v", got)
	}
	if !got.HitTopic || !got.HitConfirm {
		t.Fatalf("expected topic and confirm hits for control sample: %+v", got)
	}
}

func TestScoreInjectReplyAcceptsMarker(t *testing.T) {
	got := scoreInjectReply("INJECT_OK，我看到你提到了 cache invalidation。")
	if !got.OK {
		t.Fatalf("scoreInjectReply() should accept explicit marker: %+v", got)
	}
}
