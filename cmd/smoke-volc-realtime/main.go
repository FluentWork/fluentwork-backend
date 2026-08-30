// Package main runs B14 D2 live smoke against Volcano realtime duplex (API-Key only).
//
// Usage:
//
//	cp configs/volc.env.example .env.volc.local   # fill VOLC_SPEECH_API_KEY
//	./scripts/smoke-volc-realtime.sh
package main

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/voicepoc"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "smoke-volc-realtime FAILED: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	apiKey := firstNonEmpty(
		os.Getenv("VOLC_POC_API_KEY"),
		os.Getenv("VOLC_SPEECH_API_KEY"),
		os.Getenv("VOLC_SPEECH_API_KEY_DEV"),
	)
	if apiKey == "" {
		return fmt.Errorf("VOLC_SPEECH_API_KEY / VOLC_POC_API_KEY is empty; fill .env.volc.local")
	}

	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	cfg := voicepoc.DuplexConfig{
		APIKey:       apiKey,
		Endpoint:     strings.TrimSpace(os.Getenv("VOLC_POC_ENDPOINT")),
		Model:        firstNonEmpty(os.Getenv("VOLC_DUPLEX_MODEL"), "1.2.6.0"),
		Voice:        firstNonEmpty(os.Getenv("VOLC_DUPLEX_VOICE"), "zh_female_vv_jupiter_bigtts"),
		Instructions: "你是 FluentWork B14 D2 smoke 助手。",
	}

	result, err := voicepoc.SmokeDuplex(ctx, cfg)
	if err != nil {
		return err
	}

	enc, err := json.MarshalIndent(map[string]any{
		"issue":  "B14",
		"step":   "D2 minimal duplex session",
		"result": result,
		"next": []string{
			"T3/T4: mid-session inject behavioral check with audio/text turn",
			"T9: delay-gradient same-turn window → freeze B7 tier in meta doc 50",
			"meta #12 PREREQ: quota / no-train / concurrency",
		},
	}, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(enc))
	fmt.Println("=== B14 D2 duplex smoke PASS ===")
	return nil
}

func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return strings.TrimSpace(v)
		}
	}
	return ""
}
