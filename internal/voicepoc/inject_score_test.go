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
