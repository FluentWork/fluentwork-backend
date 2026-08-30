// Package main runs the B14 T9 injection-window harness.
//
// Default mode uses MockInjectionProvider (no Volcano credentials).
// When VOLC_POC_ENDPOINT / VOLC_POC_API_KEY are both set, the harness refuses
// to pretend live mode until a real adapter lands — it still runs mock and
// prints the credential-ready status for the execution checklist.
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
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()

	credsReady := strings.TrimSpace(os.Getenv("VOLC_POC_ENDPOINT")) != "" &&
		strings.TrimSpace(os.Getenv("VOLC_POC_API_KEY")) != ""

	provider := voicepoc.MockInjectionProvider{ModelStartAfter: 900 * time.Millisecond}
	report, err := voicepoc.RunT9(ctx, provider, nil, 6)
	if err != nil {
		return err
	}

	out := map[string]any{
		"issue":              "B14",
		"harness":            "T9 injection window",
		"live_adapter_ready": false,
		"volcano_creds_set":  credsReady,
		"report":             report,
		"blocker":            "",
		"next": []string{
			"complete meta #12 vendor PREREQ (quota / contract / concurrency)",
			"implement Volcano InjectionProvider adapter behind voicepoc.InjectionProvider",
			"re-run with live provider and write tier conclusion back to meta doc 50",
		},
	}
	if !credsReady {
		out["blocker"] = "Volcano POC credentials not set (VOLC_POC_ENDPOINT / VOLC_POC_API_KEY); mock tier is pipeline-only, not B12 freeze evidence"
	} else {
		out["blocker"] = "credentials present but live Volcano adapter not implemented yet; mock report must not freeze B12"
	}

	enc, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(enc))
	fmt.Println("=== B14 harness PASS (mock path) ===")
	fmt.Printf("mock tier: %s (p90=%dms) — NOT a live freeze until Volcano adapter runs\n",
		report.TierLabel, report.WindowP90MS)
	return nil
}
