// Package main asks Ark what model each configured endpoint actually serves.
//
// An endpoint id (ep-…) names a deployment, not a model: which model sits behind
// it is console state that only the API will tell you. That matters for pricing —
// the price table is keyed by model, and guessing the family from the deployment
// name is how a cost ledger ends up quietly wrong (see internal/orchestrator/pricing.go).
//
// Output is a JSON document, so it can be diffed against the pricing file:
//
//	set -a && source .env.volc.local && set +a
//	APP_ENV=development go run ./cmd/ark-endpoint-probe
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/config"
	"github.com/FluentWork/fluentwork-backend/internal/orchestrator"
)

// endpointEnvVars are the deployment slots this project configures.
var endpointEnvVars = []string{
	"ARK_EP_REVIEW_REFINE",
	"ARK_EP_DAILY_READ",
	"ARK_EP_TOPIC_CARD",
	"ARK_EP_HIT_MATCH",
	"ARK_EP_DRILL_JUDGE",
	"ARK_EP_TEXT_DEGRADE",
}

type probeResult struct {
	Env       string `json:"env"`
	Endpoint  string `json:"endpoint"`
	Model     string `json:"model,omitempty"`
	TokensIn  int    `json:"tokens_in,omitempty"`
	TokensOut int    `json:"tokens_out,omitempty"`
	Error     string `json:"error,omitempty"`
}

type report struct {
	ProbedAt  string        `json:"probed_at"`
	BaseURL   string        `json:"base_url"`
	Project   string        `json:"project,omitempty"`
	Endpoints []probeResult `json:"endpoints"`
	// ModelsSeen lists the distinct model names found, ready to paste into a
	// pricing file (rates still have to come from the bill).
	ModelsSeen []string `json:"models_seen"`
	// PricingGaps answers the question this tool exists for: which deployed
	// models the running price table cannot price, so the pricing file can be
	// filled from the bill. Rates are never invented here.
	PricingGaps []pricingGap `json:"pricing_gaps"`
}

// pricingGap is one model the ledger would record at 0 fen.
type pricingGap struct {
	Model  string   `json:"model"`
	Source string   `json:"price_source"`
	UsedBy []string `json:"used_by_envs"`
	Hint   string   `json:"hint"`
}

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "ark-endpoint-probe FAILED: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	cfg := config.Load()
	if strings.TrimSpace(cfg.ArkAPIKey) == "" {
		return fmt.Errorf("ARK_API_KEY / ARK_API_KEY_DEV is required (source .env.volc.local first)")
	}
	baseURL := strings.TrimRight(strings.TrimSpace(cfg.ArkBaseURL), "/")
	if baseURL == "" {
		return fmt.Errorf("ARK_BASE_URL is required")
	}

	client := &http.Client{Timeout: 30 * time.Second}
	out := report{
		ProbedAt: time.Now().UTC().Format(time.RFC3339),
		BaseURL:  baseURL,
		Project:  strings.TrimSpace(os.Getenv("ARK_PROJECT")),
	}
	seen := map[string]struct{}{}
	for _, env := range endpointEnvVars {
		endpoint := strings.TrimSpace(os.Getenv(env))
		if endpoint == "" {
			continue
		}
		result := probeOne(client, baseURL, cfg.ArkAPIKey, env, endpoint)
		out.Endpoints = append(out.Endpoints, result)
		if result.Model != "" {
			if _, ok := seen[result.Model]; !ok {
				seen[result.Model] = struct{}{}
				out.ModelsSeen = append(out.ModelsSeen, result.Model)
			}
		}
	}
	out.PricingGaps = pricingGaps(out.Endpoints)
	encoded, err := json.MarshalIndent(out, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println(string(encoded))
	return nil
}

// pricingGaps reports each deployed model the price table cannot price, with
// the env vars that depend on it. This is the actionable half of the probe: a
// model here is a model whose usage lands in ai_cost_logs at cost_fen = 0.
func pricingGaps(endpoints []probeResult) []pricingGap {
	byModel := map[string][]string{}
	order := make([]string, 0, len(endpoints))
	for _, result := range endpoints {
		if result.Model == "" {
			continue
		}
		if _, ok := byModel[result.Model]; !ok {
			order = append(order, result.Model)
		}
		byModel[result.Model] = append(byModel[result.Model], result.Env)
	}
	gaps := make([]pricingGap, 0, len(order))
	for _, model := range order {
		if _, source := orchestrator.ResolvePricing(model); source == orchestrator.PricingExact {
			continue
		}
		gaps = append(gaps, pricingGap{
			Model:  model,
			Source: string(sourceOf(model)),
			UsedBy: byModel[model],
			Hint:   "add this model to ARK_PRICING_FILE with the rates from the Volcengine bill, or its usage stays at 0 fen",
		})
	}
	if gaps == nil {
		gaps = []pricingGap{}
	}
	return gaps
}

func sourceOf(model string) orchestrator.PricingSource {
	_, source := orchestrator.ResolvePricing(model)
	return source
}

func probeOne(client *http.Client, baseURL, apiKey, env, endpoint string) probeResult {
	result := probeResult{Env: env, Endpoint: endpoint}
	body, err := json.Marshal(map[string]any{
		"model": endpoint,
		"messages": []map[string]any{
			{"role": "user", "content": "ping"},
		},
		"max_tokens":  1,
		"temperature": 0,
		// Thinking-capable deployments hang on tiny budgets with thinking on.
		"thinking": map[string]any{"type": "disabled"},
	})
	if err != nil {
		result.Error = err.Error()
		return result
	}
	ctx, cancel := context.WithTimeout(context.Background(), 30*time.Second)
	defer cancel()
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, baseURL+"/chat/completions", bytes.NewReader(body))
	if err != nil {
		result.Error = err.Error()
		return result
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+apiKey)

	res, err := client.Do(req)
	if err != nil {
		result.Error = err.Error()
		return result
	}
	defer func() { _ = res.Body.Close() }()

	var decoded struct {
		Model   string `json:"model"`
		Choices []struct {
			FinishReason string `json:"finish_reason"`
		} `json:"choices"`
		Usage *struct {
			PromptTokens     int `json:"prompt_tokens"`
			CompletionTokens int `json:"completion_tokens"`
		} `json:"usage"`
		Error *struct {
			Message string `json:"message"`
			Code    string `json:"code"`
		} `json:"error"`
	}
	if err := json.NewDecoder(res.Body).Decode(&decoded); err != nil {
		result.Error = fmt.Sprintf("http=%d decode=%v", res.StatusCode, err)
		return result
	}
	if decoded.Error != nil {
		result.Error = fmt.Sprintf("http=%d code=%s message=%s", res.StatusCode, decoded.Error.Code, decoded.Error.Message)
		return result
	}
	if res.StatusCode != http.StatusOK {
		result.Error = fmt.Sprintf("http=%d", res.StatusCode)
		return result
	}
	result.Model = strings.TrimSpace(decoded.Model)
	if decoded.Usage != nil {
		result.TokensIn = decoded.Usage.PromptTokens
		result.TokensOut = decoded.Usage.CompletionTokens
	}
	if result.Model == "" {
		result.Error = "response carried no model field"
	}
	return result
}
