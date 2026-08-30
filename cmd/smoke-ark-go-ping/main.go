// Package main runs a minimal Go HTTP smoke against the Ark review endpoint.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/config"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "smoke-ark-go-ping FAILED: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg := config.Load()
	if strings.TrimSpace(cfg.ArkAPIKey) == "" || strings.TrimSpace(cfg.ArkReviewRefineEP) == "" {
		return fmt.Errorf("ARK_API_KEY/ARK_API_KEY_DEV and ARK_EP_REVIEW_REFINE are required")
	}

	body, err := json.Marshal(map[string]any{
		"model": cfg.ArkReviewRefineEP,
		"messages": []map[string]any{
			{"role": "user", "content": "Reply with exactly: PONG"},
		},
		"max_tokens":  16,
		"temperature": 0,
	})
	if err != nil {
		return err
	}

	client := &http.Client{
		Timeout: 45 * time.Second,
		Transport: &http.Transport{
			Proxy:               http.ProxyFromEnvironment,
			ForceAttemptHTTP2:   false,
			DialContext:         (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext,
			TLSHandshakeTimeout: 15 * time.Second,
		},
	}
	ctx, cancel := context.WithTimeout(context.Background(), 45*time.Second)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodPost, strings.TrimRight(cfg.ArkBaseURL, "/")+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+cfg.ArkAPIKey)

	resp, err := client.Do(req)
	if err != nil {
		return err
	}
	defer func() { _ = resp.Body.Close() }()

	var decoded map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&decoded); err != nil {
		return fmt.Errorf("decode response: %w", err)
	}
	encoded, err := json.MarshalIndent(decoded, "", "  ")
	if err != nil {
		return err
	}
	fmt.Printf("=== Ark Go ping HTTP %d ===\n%s\n", resp.StatusCode, string(encoded))
	return nil
}
