// Package main runs B14 live smokes against Volcano realtime duplex (API-Key only).
//
// Modes:
//
//	(default) D2: session.create + session.update
//	--asr      D3/T2: ASR transcript (V1)
//	--inject   T3/T4: same-turn vs next-turn inject effect
//	--t9       D5/T9: delay-gradient window → B7 tier
//
// Usage:
//
//	./scripts/smoke-volc-realtime.sh
//	./scripts/smoke-volc-realtime.sh --asr
//	./scripts/smoke-volc-realtime.sh --inject
//	./scripts/smoke-volc-realtime.sh --t9
package main

import (
	"context"
	"encoding/json"
	"flag"
	"fmt"
	"log/slog"
	"os"
	"strconv"
	"strings"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/voicepoc"
	"github.com/FluentWork/fluentwork-backend/pkg/logx"
)

func main() {
	slog.SetDefault(logx.New("smoke-volc-realtime"))
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "smoke-volc-realtime FAILED: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	asr := flag.Bool("asr", false, "run D3/T2 ASR transcript smoke (V1)")
	inject := flag.Bool("inject", false, "run T3/T4 inject smoke (V3)")
	t9 := flag.Bool("t9", false, "run D5/T9 delay-gradient window (V8)")
	wav := flag.String("wav", "", "path to 16kHz mono PCM WAV")
	flag.Parse()

	modes := 0
	for _, v := range []bool{*asr, *inject, *t9} {
		if v {
			modes++
		}
	}
	if modes > 1 {
		return fmt.Errorf("use only one of --asr / --inject / --t9")
	}

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
		Logger:   slog.Default().With("component", "voicepoc.cli"),
	}

	wavPath := strings.TrimSpace(*wav)
	if wavPath == "" {
		wavPath = voicepoc.DefaultFixtureWAVPath()
	}

	var (
		payload map[string]any
		err     error
		pass    string
	)

	switch {
	case *t9:
		trialsPerDelay := 1
		if v := strings.TrimSpace(os.Getenv("VOLC_T9_TRIALS")); v != "" {
			n, e := strconv.Atoi(v)
			if e != nil || n <= 0 {
				return fmt.Errorf("VOLC_T9_TRIALS must be positive int")
			}
			trialsPerDelay = n
		}
		// Full gradient is expensive; default one trial per delay.
		timeout := time.Duration(trialsPerDelay*5*3)*time.Minute + 2*time.Minute
		ctx, cancel := context.WithTimeout(context.Background(), timeout)
		defer cancel()
		report, e := voicepoc.SmokeDuplexT9(ctx, cfg, wavPath, nil, trialsPerDelay)
		err = e
		payload = map[string]any{
			"issue":  "B14",
			"step":   "D5/T9 injection window (V8)",
			"report": report,
			"next": []string{
				"Write tier conclusion into meta doc 50 appendix",
				"meta #12 PREREQ before treating freeze as production-binding",
				"Optional: VOLC_T9_TRIALS=6 for fuller P90 confidence",
			},
		}
		pass = "=== B14 T9 window smoke PASS ==="
		if err == nil {
			fmt.Fprintf(os.Stderr, "T9 tier=%s same_hit=%.2f next_hit=%.2f p90=%dms\n",
				report.TierLabel, report.SameTurnHitRate, report.NextTurnHitRate, report.WindowP90MS)
		}
	case *inject:
		ctx, cancel := context.WithTimeout(context.Background(), 120*time.Second)
		defer cancel()
		result, e := voicepoc.SmokeDuplexInject(ctx, cfg, wavPath)
		err = e
		payload = map[string]any{
			"issue":  "B14",
			"step":   "T3/T4 inject-after-commit (V3/V5 probe)",
			"result": result,
			"next": []string{
				"./scripts/smoke-volc-realtime.sh --t9",
				"meta #12 PREREQ",
			},
		}
		pass = "=== B14 T3/T4 inject smoke PASS ==="
	case *asr:
		ctx, cancel := context.WithTimeout(context.Background(), 90*time.Second)
		defer cancel()
		cfg.Instructions = "你是 FluentWork B14 ASR smoke 助手。用一句中文简短回应用户。"
		result, e := voicepoc.SmokeDuplexASR(ctx, cfg, wavPath)
		err = e
		payload = map[string]any{
			"issue":  "B14",
			"step":   "D3/T2 ASR transcript (V1)",
			"result": result,
			"next":   []string{"./scripts/smoke-volc-realtime.sh --inject"},
		}
		pass = "=== B14 D3 ASR smoke PASS ==="
	default:
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		cfg.Instructions = "你是 FluentWork B14 D2 smoke 助手。"
		result, e := voicepoc.SmokeDuplex(ctx, cfg)
		err = e
		payload = map[string]any{
			"issue":  "B14",
			"step":   "D2 minimal duplex session",
			"result": result,
			"next":   []string{"./scripts/smoke-volc-realtime.sh --asr"},
		}
		pass = "=== B14 D2 duplex smoke PASS ==="
	}

	if err != nil {
		payload["error"] = err.Error()
		_ = printJSON(payload)
		return err
	}
	if err := printJSON(payload); err != nil {
		return err
	}
	fmt.Println(pass)
	return nil
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
