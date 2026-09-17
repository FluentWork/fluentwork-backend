package orchestrator

import "testing"

func TestCalculateCost(t *testing.T) {
	tests := []struct {
		name         string
		model        string
		promptTokens int
		outputTokens int
		// pricing, when set, replaces the table for this case — the state
		// ARK_PRICING_FILE produces in production.
		pricing      map[string]ModelPricing
		expectedCost int
	}{
		{
			name:         "doubao-mini-32k (Ark Mini) - most economical",
			model:        "doubao-mini-32k",
			promptTokens: 1000,
			outputTokens: 500,
			expectedCost: 6, // (1000*3)/1000 + (500*6)/1000 = 3 + 3 = 6
		},
		{
			// The endpoint really serves doubao-seed-2-1-pro-260628 (probed
			// 2026-09-18), and the built-in table has no entry for it. The row
			// is recorded at 0 and counted as unpriced — the honest answer until
			// the bill supplies a rate, not a rate borrowed from another model.
			name:         "endpoint id resolves to a model the table does not price",
			model:        "ep-20260830204651-pffhf",
			promptTokens: 2000,
			outputTokens: 1000,
			expectedCost: 0,
		},
		{
			// Once the pricing file carries the deployed model, the same call is
			// priced exactly — this is the state ARK_PRICING_FILE exists for.
			name:         "deployed model name with a price entry",
			model:        "doubao-seed-2-1-pro-260628",
			promptTokens: 2000,
			outputTokens: 1000,
			pricing:      map[string]ModelPricing{"doubao-seed-2-1-pro-260628": {InputPricePerKToken: 3, OutputPricePerKToken: 6}},
			expectedCost: 12,
		},
		{
			name:         "doubao-pro-32k standard usage",
			model:        "doubao-pro-32k",
			promptTokens: 1000,
			outputTokens: 500,
			expectedCost: 9, // (1000*5)/1000 + (500*9)/1000 = 5 + 4 = 9
		},
		{
			name:         "doubao-lite-32k same as mini",
			model:        "doubao-lite-32k",
			promptTokens: 1000,
			outputTokens: 500,
			expectedCost: 6, // (1000*3)/1000 + (500*6)/1000 = 3 + 3 = 6
		},
		{
			name:         "doubao-pro-4k higher pricing",
			model:        "doubao-pro-4k",
			promptTokens: 1000,
			outputTokens: 500,
			expectedCost: 12, // (1000*8)/1000 + (500*8)/1000 = 8 + 4 = 12
		},
		{
			name:         "zero tokens returns zero cost",
			model:        "doubao-mini-32k",
			promptTokens: 0,
			outputTokens: 0,
			expectedCost: 0,
		},
		{
			name:         "small usage rounds up to 1 fen minimum",
			model:        "doubao-mini-32k",
			promptTokens: 10,
			outputTokens: 10,
			expectedCost: 1, // Too small to round to 0, returns 1
		},
		{
			// A model no rule recognises is recorded at 0 and counted, not
			// priced as whatever family its name resembles: a plausible wrong
			// number is worse than a visible gap.
			name:         "unknown model is not priced",
			model:        "unknown-model",
			promptTokens: 1000,
			outputTokens: 500,
			expectedCost: 0,
		},
		{
			name:         "doubao-pro-256k premium pricing",
			model:        "doubao-pro-256k",
			promptTokens: 1000,
			outputTokens: 1000,
			expectedCost: 80, // (1000*20)/1000 + (1000*60)/1000 = 20 + 60 = 80
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			if tt.pricing != nil {
				if err := ConfigurePricing(tt.pricing); err != nil {
					t.Fatalf("ConfigurePricing: %v", err)
				}
				defer restoreDefaultPricing(t)
			}
			cost := CalculateCost(tt.model, tt.promptTokens, tt.outputTokens)
			if cost != tt.expectedCost {
				t.Errorf("CalculateCost(%q, %d, %d) = %d; want %d",
					tt.model, tt.promptTokens, tt.outputTokens, cost, tt.expectedCost)
			}
		})
	}
}

func TestNormalizeModelName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		// Endpoint → model, as probed from the API on 2026-09-18
		// (cmd/ark-endpoint-probe). These are the deployed models, not a guess
		// from the deployment's name.
		{"ep-20260830204651-pffhf", "doubao-seed-2-1-pro-260628"},   // ARK_EP_REVIEW_REFINE
		{"ep-20260830204818-8kdfr", "doubao-seed-2-1-pro-260628"},   // ARK_EP_DAILY_READ
		{"ep-20260830204912-wtjw9", "doubao-seed-2-1-pro-260628"},   // ARK_EP_TOPIC_CARD
		{"ep-20260830205333-prddb", "doubao-seed-2-1-turbo-260628"}, // ARK_EP_HIT_MATCH
		{"ep-20260830205423-xg4pd", "doubao-seed-2-1-turbo-260628"}, // ARK_EP_DRILL_JUDGE
		{"ep-20260830205520-d9d8n", "doubao-seed-2-1-turbo-260628"}, // ARK_EP_TEXT_DEGRADE

		// Case insensitivity
		{"EP-20260830204651-PFFHF", "doubao-seed-2-1-pro-260628"},

		// Direct model names
		{"doubao-mini-32k", "doubao-mini-32k"},
		{"Doubao-Mini-32K", "doubao-mini-32k"},
		{"doubao-pro-32k", "doubao-pro-32k"},
		{"Doubao-Pro-32K", "doubao-pro-32k"},
		{"doubao-lite-128k", "doubao-lite-32k"},
		{"doubao-character-4k", "doubao-character-32k"},
		{"doubao-pro-4k", "doubao-pro-4k"},
		{"doubao-pro-128k", "doubao-pro-128k"},
		{"doubao-pro-256k", "doubao-pro-256k"},
		{"doubao-pro-search", "doubao-pro-search"},

		// Model name patterns
		{"some-model-with-mini", "doubao-mini-32k"},
		{"some-model-with-lite", "doubao-lite-32k"},

		// No family matches: the name is returned unchanged, and CalculateCost
		// records it as unpriced rather than guessing.
		{"unknown", "unknown"},
		{"", ""},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := normalizeModelName(tt.input)
			if result != tt.expected {
				t.Errorf("normalizeModelName(%q) = %q; want %q", tt.input, result, tt.expected)
			}
		})
	}
}
