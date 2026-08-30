// Package main runs B14 live smokes against Volcano realtime duplex (API-Key only).
//
// Modes:
//
//	(default) D2: session.create + session.update
//	--asr      D3/T2: upload fixture WAV and require ASR transcript (V1)
//
// Usage:
//
//	./scripts/smoke-volc-realtime.sh
//	./scripts/smoke-volc-realtime.sh --asr
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"os"
	"path/filepath"
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
	asr := flag.Bool("asr", false, "run D3/T2 ASR transcript smoke (V1)")
	wav := flag.String("wav", "", "path to 16kHz mono PCM WAV (default: voicepoc testdata fixture)")
	flag.Parse()

	apiKey := firstNonEmpty(
		os.Getenv("VOLC_POC_API_KEY"),
		os.Getenv("VOLC_SPEECH_API_KEY"),
		os.Getenv("VOLC_SPEECH_API_KEY_DEV"),
	)
	if apiKey == "" {
		return fmt.Errorf("VOLC_SPEECH_API_KEY / VOLC_POC_API_KEY is empty; fill .env.volc.local")
	}

	cfg := voicepoc.DuplexConfig{
		APIKey:   apiKey,
		Endpoint: strings.TrimSpace(os.Getenv("VOLC_POC_ENDPOINT")),
		Model:    firstNonEmpty(os.Getenv("VOLC_DUPLEX_MODEL"), "1.2.6.0"),
		Voice:    firstNonEmpty(os.Getenv("VOLC_DUPLEX_VOICE"), "zh_female_vv_jupiter_bigtts"),
	}

	ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
	defer cancel()

	var (
		result map[string]any
		err    error
		step   string
		next   []string
	)

	if *asr {
		step = "D3/T2 ASR transcript (V1)"
		wavPath := strings.TrimSpace(*wav)
		if wavPath == "" {
			wavPath = defaultFixtureWAV()
		}
		cfg.Instructions = "你是 FluentWork B14 ASR smoke 助手。用一句中文简短回应用户。"
		result, err = voicepoc.SmokeDuplexASR(ctx, cfg, wavPath)
		next = []string{
			"T3/T4: inject after ASR and check assistant confirmation (V3/V5)",
			"T9: delay-gradient same-turn window → freeze B7 tier",
			"meta #12 PREREQ: quota / no-train / concurrency",
		}
	} else {
		step = "D2 minimal duplex session"
		cfg.Instructions = "你是 FluentWork B14 D2 smoke 助手。"
		result, err = voicepoc.SmokeDuplex(ctx, cfg)
		next = []string{
			"D3/T2: ./scripts/smoke-volc-realtime.sh --asr",
			"T3/T4: mid-session inject behavioral check with audio turn",
			"T9: delay-gradient same-turn window → freeze B7 tier in meta doc 50",
		}
	}
	if err != nil {
		if result != nil {
			_ = printJSON(map[string]any{"issue": "B14", "step": step, "result": result, "error": err.Error()})
		}
		return err
	}

	if err := printJSON(map[string]any{
		"issue":  "B14",
		"step":   step,
		"result": result,
		"next":   next,
	}); err != nil {
		return err
	}
	if *asr {
		fmt.Println("=== B14 D3 ASR smoke PASS ===")
	} else {
		fmt.Println("=== B14 D2 duplex smoke PASS ===")
	}
	return nil
}

func defaultFixtureWAV() string {
	// cmd/smoke-volc-realtime → repo root → internal/voicepoc/testdata/...
	return filepath.Clean(filepath.Join("internal", "voicepoc", "testdata", "cache_invalidation_16k.wav"))
}

func printJSON(v any) error {
	enc, err := json.MarshalIndent(v, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(enc))
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
