// Package main tests orchestrator.Client connectivity and review generation.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/config"
	"github.com/FluentWork/fluentwork-backend/internal/orchestrator"
	"github.com/FluentWork/fluentwork-backend/internal/reviewgen"
)

func main() {
	if err := run(); err != nil {
		log.Fatalf("Error: %v", err)
	}
}

func run() error {
	cfg := config.Load()

	fmt.Println("=== Orchestrator Connectivity Test ===")
	fmt.Printf("ARK_API_KEY: %s\n", maskKey(cfg.ArkAPIKey))
	fmt.Printf("ARK_BASE_URL: %s\n", cfg.ArkBaseURL)
	fmt.Printf("ARK_REVIEW_REFINE_EP: %s\n", cfg.ArkReviewRefineEP)
	fmt.Println()

	// 测试 1: 创建 orchestrator client
	fmt.Println("Test 1: Creating orchestrator client...")
	client := orchestrator.NewClient(cfg, nil)
	if client == nil {
		return fmt.Errorf("orchestrator.NewClient returned nil")
	}
	fmt.Println("✓ Client created successfully")
	fmt.Println()

	// 测试 2: 创建 OrchestratorAdapter
	fmt.Println("Test 2: Creating OrchestratorAdapter...")
	adapter := &reviewgen.OrchestratorAdapter{
		Client: client,
	}

	if !adapter.Enabled() {
		return fmt.Errorf("OrchestratorAdapter.Enabled() = false, expected true")
	}
	fmt.Println("✓ OrchestratorAdapter enabled")
	fmt.Println()

	// 测试 3: 生成 review（真实 API 调用）
	fmt.Println("Test 3: Generating review (real API call)...")
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	req := reviewgen.Request{
		SessionID: "test-session-" + time.Now().Format("20060102-150405"),
		UserID:    "test-user",
		SceneType: "standup",
		Transcript: `AI: Good morning! Ready for your standup practice?
User: Yeah, let's do it.
AI: Great! Go ahead and share your update.
User: So yesterday I finished the API review. Today I'm going to sync up with the team about the deployment timeline.
AI: Excellent update! Let me know if you need any feedback.`,
	}

	fmt.Printf("  Session ID: %s\n", req.SessionID)
	fmt.Printf("  Transcript length: %d bytes\n", len(req.Transcript))
	fmt.Println()

	result, err := adapter.Generate(ctx, req)
	if err != nil {
		return fmt.Errorf("Generate failed: %w", err)
	}

	fmt.Println("✓ Review generated successfully!")
	fmt.Printf("  Generator: %s\n", result.Generator)
	fmt.Printf("  Model: %s\n", result.Model)
	fmt.Printf("  Tokens in: %d\n", result.TokensIn)
	fmt.Printf("  Tokens out: %d\n", result.TokensOut)
	fmt.Println()

	// 测试 4: 验证 review 内容
	fmt.Println("Test 4: Validating review content...")

	var review map[string]any
	if err := json.Unmarshal(result.Review, &review); err != nil {
		return fmt.Errorf("failed to parse review JSON: %w", err)
	}

	prettyReview, _ := json.MarshalIndent(review, "", "  ")
	fmt.Println("Review JSON:")
	fmt.Println(string(prettyReview))
	fmt.Println()

	var refine map[string]any
	if err := json.Unmarshal(result.Refine, &refine); err != nil {
		return fmt.Errorf("failed to parse refine JSON: %w", err)
	}

	prettyRefine, _ := json.MarshalIndent(refine, "", "  ")
	fmt.Println("Refine JSON:")
	fmt.Println(string(prettyRefine))
	fmt.Println()

	// 验证必需字段
	if _, ok := review["goal_achievement"]; !ok {
		return fmt.Errorf("review missing 'goal_achievement' field")
	}
	if _, ok := review["issues"]; !ok {
		return fmt.Errorf("review missing 'issues' field")
	}
	if _, ok := review["suggestions"]; !ok {
		return fmt.Errorf("review missing 'suggestions' field")
	}
	if _, ok := review["comparisons"]; !ok {
		return fmt.Errorf("review missing 'comparisons' field")
	}

	blocks, ok := refine["blocks"].([]any)
	if !ok {
		return fmt.Errorf("refine missing 'blocks' array")
	}

	fmt.Printf("✓ Review validation passed\n")
	fmt.Printf("  - goal_achievement: present\n")
	fmt.Printf("  - issues: %d items\n", len(review["issues"].([]any)))
	fmt.Printf("  - suggestions: %d items\n", len(review["suggestions"].([]any)))
	fmt.Printf("  - comparisons: %d items\n", len(review["comparisons"].([]any)))
	fmt.Printf("  - phrase blocks: %d items\n", len(blocks))
	fmt.Println()

	fmt.Println("=== All Tests Passed! ===")
	return nil
}

func maskKey(key string) string {
	if key == "" {
		return "<NOT SET>"
	}
	if len(key) <= 12 {
		return "***"
	}
	return key[:8] + "..." + key[len(key)-4:]
}
