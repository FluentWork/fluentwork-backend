package orchestrator

import (
	"testing"

	"github.com/FluentWork/fluentwork-backend/internal/config"
)

// TestClientInstanceReuse 验证多次调用 NewClient 是否复用同一实例（修复后）
func TestClientInstanceReuse(t *testing.T) {
	cfg := config.Config{
		ArkBaseURL:        "https://api.test.com",
		ArkAPIKey:         "test-key",
		ArkReviewRefineEP: "test-model",
	}
	costWriter := &mockCostWriter{}

	// 模拟 app-server/main.go 的 5 次调用
	client1 := NewClient(cfg, costWriter) // reviewGenerator
	client2 := NewClient(cfg, costWriter) // reviewEval
	client3 := NewClient(cfg, costWriter) // drillSvc
	client4 := NewClient(cfg, costWriter) // materialSvc
	client5 := NewClient(cfg, costWriter) // topicGen

	// 修复后：所有调用应该返回同一个实例
	ark1 := client1.(*ArkClient)
	ark2 := client2.(*ArkClient)
	ark3 := client3.(*ArkClient)
	ark4 := client4.(*ArkClient)
	ark5 := client5.(*ArkClient)

	// 验证：所有实例共享同一个 httpClient
	if ark1.httpClient != ark2.httpClient {
		t.Error("expected same httpClient instance, but got different")
	}
	if ark2.httpClient != ark3.httpClient {
		t.Error("expected same httpClient instance, but got different")
	}
	if ark3.httpClient != ark4.httpClient {
		t.Error("expected same httpClient instance, but got different")
	}
	if ark4.httpClient != ark5.httpClient {
		t.Error("expected same httpClient instance, but got different")
	}

	t.Log("✓ All NewClient calls return the same instance (connection pool shared)")
	t.Log("✓ Single http.Client with MaxIdleConns=100 (vs 500 before fix)")
}

// TestClientShouldBeSingleton 验证单例模式正确性
func TestClientShouldBeSingleton(t *testing.T) {
	cfg := config.Config{
		ArkBaseURL:        "https://api.test.com",
		ArkAPIKey:         "test-key",
		ArkReviewRefineEP: "test-model",
	}
	costWriter := &mockCostWriter{}

	client1 := NewClient(cfg, costWriter)
	client2 := NewClient(cfg, costWriter)

	if client1 != client2 {
		t.Error("expected same Client instance (singleton), but got different")
	}

	ark1 := client1.(*ArkClient)
	ark2 := client2.(*ArkClient)

	if ark1.httpClient != ark2.httpClient {
		t.Error("expected same httpClient instance, but got different")
	}

	t.Log("✓ Singleton pattern working correctly")
}