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
		fmt.Fprintf(os.Stderr, "poc-injection-window FAILED: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	apiKey := firstNonEmpty(
		os.Getenv("VOLC_POC_API_KEY"),
		os.Getenv("VOLC_SPEECH_API_KEY"),
		os.Getenv("VOLC_SPEECH_API_KEY_DEV"),
	)
	liveRequested := apiKey != "" || strings.TrimSpace(os.Getenv("VOLC_POC_ENDPOINT")) != ""
	liveT9 := strings.EqualFold(os.Getenv("VOLC_POC_LIVE_T9"), "1") ||
		strings.EqualFold(os.Getenv("VOLC_POC_LIVE_T9"), "true")

	out := map[string]any{
		"issue":   "B14",
		"harness": "T9 injection window",
	}

	if liveRequested && apiKey != "" {
		ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
		defer cancel()
		smoke, err := voicepoc.SmokeDuplex(ctx, voicepoc.DuplexConfig{
			APIKey:       apiKey,
			Endpoint:     strings.TrimSpace(os.Getenv("VOLC_POC_ENDPOINT")),
			Instructions: "你是 FluentWork B14 harness 助手。",
		})
		if err != nil {
			return fmt.Errorf("live duplex smoke: %w", err)
		}
		out["live_adapter_ready"] = true
		out["volcano_creds_set"] = true
		out["d2_smoke"] = smoke
	} else {
		out["live_adapter_ready"] = false
		out["volcano_creds_set"] = false
	}

	ctx, cancel := context.WithTimeout(context.Background(), 2*time.Minute)
	defer cancel()

	var (
		report voicepoc.WindowReport
		err    error
	)
	if liveT9 && apiKey != "" {
		provider := voicepoc.VolcDuplexInjectionProvider{Config: voicepoc.DuplexConfig{
			APIKey:   apiKey,
			Endpoint: strings.TrimSpace(os.Getenv("VOLC_POC_ENDPOINT")),
		}}
		// Sparse gradient for live cost control; full ≥6×5 after audio observation lands.
		delays := []time.Duration{
			200 * time.Millisecond,
			600 * time.Millisecond,
			1000 * time.Millisecond,
		}
		report, err = voicepoc.RunT9(ctx, provider, delays, 1)
		out["t9_mode"] = "live-channel-probe"
		out["blocker"] = "live T9 currently probes session.update after delay only; same-turn audio observation not yet frozen for B12"
	} else {
		provider := voicepoc.MockInjectionProvider{ModelStartAfter: 900 * time.Millisecond}
		report, err = voicepoc.RunT9(ctx, provider, nil, 6)
		out["t9_mode"] = "mock"
		if !liveRequested {
			out["blocker"] = "Volcano POC credentials not set; mock tier is pipeline-only, not B12 freeze evidence"
		} else {
			out["blocker"] = "D2 live smoke available; set VOLC_POC_LIVE_T9=1 for live channel-delay probe (still not B12 freeze until audio T9)"
		}
	}
	if err != nil {
		return err
	}
	out["report"] = report
	out["next"] = []string{
		"complete meta #12 vendor PREREQ (quota / contract / concurrency)",
		"add audio-turn same-turn observation to VolcDuplexInjectionProvider",
		"re-run live T9 and write tier conclusion back to meta doc 50",
	}

	enc, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(enc))
	fmt.Printf("=== B14 harness PASS (%s) ===\n", out["t9_mode"])
	fmt.Printf("tier: %s (p90=%dms) credential_mode=%s\n", report.TierLabel, report.WindowP90MS, report.CredentialMode)
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
