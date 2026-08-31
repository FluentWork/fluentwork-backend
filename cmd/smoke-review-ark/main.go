// Package main runs a live B8 review/refine smoke against the configured Ark endpoint.
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"os"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/config"
	"github.com/FluentWork/fluentwork-backend/internal/reviewgen"
	"github.com/FluentWork/fluentwork-backend/pkg/logx"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "smoke-review-ark FAILED: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg := config.Load()
	logger := logx.New("smoke-review-ark")
	slog.SetDefault(logger)

	gen := reviewgen.ArkGenerator{
		BaseURL:  cfg.ArkBaseURL,
		APIKey:   cfg.ArkAPIKey,
		Endpoint: cfg.ArkReviewRefineEP,
		Logger:   logger.With("component", "reviewgen.ark"),
	}
	if !gen.Enabled() {
		return fmt.Errorf("ark review generator is not configured; require ARK_API_KEY/ARK_API_KEY_DEV and ARK_EP_REVIEW_REFINE")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
	defer cancel()

	req := reviewgen.Request{
		SessionID:  "smoke-b8-review-ark",
		SceneType:  "standup",
		Transcript: "I will sync up with the team tomorrow. I am blocked on the API review.",
	}
	result, err := gen.Generate(ctx, req)
	if err != nil {
		return err
	}

	out := map[string]any{
		"provider":      "ark",
		"generator":     result.Generator,
		"model":         result.Model,
		"tokens_in":     result.TokensIn,
		"tokens_out":    result.TokensOut,
		"review":        json.RawMessage(result.Review),
		"refine":        json.RawMessage(result.Refine),
		"sample_id":     req.SessionID,
		"scene_type":    req.SceneType,
		"transcript":    req.Transcript,
		"live_smoke":    true,
		"validated_b15": true,
	}
	encoded, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println("=== B8 Ark review/refine smoke PASS ===")
	fmt.Println(string(encoded))
	return nil
}
