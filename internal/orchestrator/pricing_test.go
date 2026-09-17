package orchestrator

import "testing"

// Money is recorded in 微元 (10^-6 CNY): a model priced under 10 CNY per million
// costs less than one 分 for a single call, which integer fen cannot express.
func TestCalculateCostMicroYuan(t *testing.T) {
	tests := []struct {
		name         string
		model        string
		promptTokens int
		outputTokens int
		// pricing, when set, replaces the table for this case — the state
		// ARK_PRICING_FILE produces in production.
		pricing      map[string]ModelPricing
		expectedCost int64
	}{
		{
			name:         "doubao-mini-32k, the one built-in rate with a source",
			model:        "doubao-mini-32k",
			promptTokens: 1000,
			outputTokens: 500,
			// 0.3 CNY/1M in, 0.6 CNY/1M out → (1000*300000 + 500*600000)/1e6 = 600 微元.
			expectedCost: 600,
		},
		{
			// The endpoint really serves doubao-seed-2-1-pro-260628 (probed
			// 2026-09-18) and the built-in table has no entry for it. The row is
			// recorded at 0 and counted as unpriced — the honest answer until the
			// bill supplies a rate, not a rate borrowed from another model.
			name:         "endpoint id resolves to a model the table does not price",
			model:        "ep-20260830204651-pffhf",
			promptTokens: 2000,
			outputTokens: 1000,
			expectedCost: 0,
		},
		{
			// Once the pricing file carries the deployed model, the same call is
			// priced exactly — the state ARK_PRICING_FILE exists for.
			name:         "deployed model name with a price entry",
			model:        "doubao-seed-2-1-pro-260628",
			promptTokens: 2000,
			outputTokens: 1000,
			pricing: map[string]ModelPricing{
				"doubao-seed-2-1-pro-260628": {InputMicroYuanPerMillion: 800_000, OutputMicroYuanPerMillion: 2_000_000},
			},
			expectedCost: 3600, // (2000*0.8 + 1000*2.0) 元/1M → 3.6e-3 元 = 3600 微元
		},
		{
			// The old table's other entries were dropped: their numbers were 分
			// per 1K and contradicted their own comments by 100x, with no source
			// recorded. They are unpriced rather than silently wrong.
			name:         "legacy family entry dropped from the table",
			model:        "doubao-pro-32k",
			promptTokens: 1000,
			outputTokens: 500,
			expectedCost: 0,
		},
		{
			name:         "zero tokens returns zero cost",
			model:        "doubao-mini-32k",
			promptTokens: 0,
			outputTokens: 0,
			expectedCost: 0,
		},
		{
			name:         "usage that rounds below a microunit keeps a 1 微元 floor",
			model:        "doubao-mini-32k",
			promptTokens: 1,
			outputTokens: 1,
			expectedCost: 1,
		},
		{
			// A model no rule recognises is recorded at 0 and counted, not priced
			// as whatever family its name resembles: a plausible wrong number is
			// worse than a visible gap.
			name:         "unknown model is not priced",
			model:        "unknown-model",
			promptTokens: 1000,
			outputTokens: 500,
			expectedCost: 0,
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
			cost := CalculateCostMicroYuan(tt.model, tt.promptTokens, tt.outputTokens)
			if cost != tt.expectedCost {
				t.Errorf("CalculateCostMicroYuan(%q, %d, %d) = %d; want %d",
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
