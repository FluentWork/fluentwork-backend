// Package main runs the B15 offline prompt regression baseline.
package main

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"

	"github.com/FluentWork/fluentwork-backend/internal/eval"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "eval-prompt-regression FAILED: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	root, err := os.Getwd()
	if err != nil {
		return err
	}
	path := filepath.Join(root, "eval", "offline", "samples", "wave2-synth-v1.json")
	if len(os.Args) > 1 {
		path = os.Args[1]
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return err
	}
	var samples []eval.Sample
	if err := json.Unmarshal(raw, &samples); err != nil {
		return fmt.Errorf("decode %s: %w", path, err)
	}
	res := eval.RunDataset(samples)
	enc, err := json.MarshalIndent(res, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(enc))
	if res.Failed > 0 {
		return fmt.Errorf("%d/%d samples failed", res.Failed, res.Samples)
	}
	fmt.Printf("=== B15 prompt regression PASS (%d samples) ===\n", res.Samples)
	return nil
}
