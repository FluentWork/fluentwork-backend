package orchestrator

import (
	"encoding/json"
	"fmt"
	"os"
	"strings"
	"sync"
)

// ModelPricing 定义模型的输入/输出 token 单价（单位：分/千 tokens）
type ModelPricing struct {
	InputPricePerKToken  int // 输入 token 单价（分/1K tokens）
	OutputPricePerKToken int // 输出 token 单价（分/1K tokens）
}

// defaultArkPricing is the built-in 火山 Ark price table (2026-09 data).
// 参考：https://www.volcengine.com/docs/82379/1099320
//
// It is a *default*, not the source of truth: P2-2 settled that vendor billing
// decides the rates, and doc 79 asked for the table to be replaceable without a
// deploy. LoadPricingFile does that — ops corrects the numbers, not the code.
func defaultArkPricing() map[string]ModelPricing {
	return map[string]ModelPricing{
		// Doubao-mini 系列（Ark Mini，最经济的模型）
		// 0.3元/M input tokens = 3分/1K tokens
		// 0.6元/M output tokens = 6分/1K tokens
		"doubao-mini-32k": {InputPricePerKToken: 3, OutputPricePerKToken: 6},

		// Doubao-pro 系列
		"doubao-pro-4k":     {InputPricePerKToken: 8, OutputPricePerKToken: 8},
		"doubao-pro-32k":    {InputPricePerKToken: 5, OutputPricePerKToken: 9},
		"doubao-pro-128k":   {InputPricePerKToken: 5, OutputPricePerKToken: 9},
		"doubao-pro-256k":   {InputPricePerKToken: 20, OutputPricePerKToken: 60},
		"doubao-pro-search": {InputPricePerKToken: 10, OutputPricePerKToken: 10},

		// Doubao-lite 系列
		"doubao-lite-4k":   {InputPricePerKToken: 3, OutputPricePerKToken: 6},
		"doubao-lite-32k":  {InputPricePerKToken: 3, OutputPricePerKToken: 6},
		"doubao-lite-128k": {InputPricePerKToken: 3, OutputPricePerKToken: 6},

		// Character 系列
		"doubao-character-4k":   {InputPricePerKToken: 8, OutputPricePerKToken: 8},
		"doubao-character-32k":  {InputPricePerKToken: 5, OutputPricePerKToken: 9},
		"doubao-character-128k": {InputPricePerKToken: 5, OutputPricePerKToken: 9},
	}
}

// PricingSource says how a model's price was found. The difference matters: an
// exact entry is a fact ops set, a family guess is our inference from the model
// name, and neither is an invoice.
type PricingSource string

const (
	// PricingExact is an exact table hit, including an endpoint-id mapping.
	PricingExact PricingSource = "exact"
	// PricingHeuristic is a price inferred from the model name's family.
	PricingHeuristic PricingSource = "heuristic"
	// PricingUnknown is a model no rule recognised: it is recorded at 0 rather
	// than at a guessed rate.
	PricingUnknown PricingSource = "unknown"
)

var (
	pricingMu  sync.RWMutex
	arkPricing = defaultArkPricing()
)

// ConfigurePricing replaces the price table (doc 79). A malformed table is
// refused rather than merged: an ops typo must not leave half the models priced
// by the file and half by the built-in defaults.
func ConfigurePricing(table map[string]ModelPricing) error {
	if len(table) == 0 {
		return fmt.Errorf("pricing table is empty")
	}
	cleaned := make(map[string]ModelPricing, len(table))
	for model, price := range table {
		name := strings.ToLower(strings.TrimSpace(model))
		if name == "" {
			return fmt.Errorf("pricing table has an empty model name")
		}
		// Zero is refused as well as negative: a rate nobody knows is expressed
		// by leaving the model out (it then records 0 fen and is counted as
		// unpriced), not by writing a zero that silently prices every call at
		// the 1-fen floor.
		if price.InputPricePerKToken <= 0 || price.OutputPricePerKToken <= 0 {
			return fmt.Errorf("pricing for %q must be positive; omit the model instead of pricing it at 0", model)
		}
		cleaned[name] = price
	}
	pricingMu.Lock()
	arkPricing = cleaned
	pricingMu.Unlock()
	return nil
}

// pricingFileEntry is the on-disk shape of one model's prices.
type pricingFileEntry struct {
	InputPricePerKToken  int `json:"input_price_per_k_token"`
	OutputPricePerKToken int `json:"output_price_per_k_token"`
}

// LoadPricingFile reads an override table from JSON, in the shape
// {"doubao-mini-32k": {"input_price_per_k_token": 3, "output_price_per_k_token": 6}}.
//
// The caller fails startup on error on purpose: a pricing file that does not
// parse means somebody intended to change the rates, and quietly running on the
// defaults would bill at numbers nobody chose.
func LoadPricingFile(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read pricing file: %w", err)
	}
	var parsed map[string]pricingFileEntry
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return fmt.Errorf("parse pricing file: %w", err)
	}
	table := make(map[string]ModelPricing, len(parsed))
	for model, entry := range parsed {
		table[model] = ModelPricing(entry)
	}
	return ConfigurePricing(table)
}

// PricingTable returns a copy of the active table, for tests and for anybody
// asking what rates this process is actually using.
func PricingTable() map[string]ModelPricing {
	pricingMu.RLock()
	defer pricingMu.RUnlock()
	out := make(map[string]ModelPricing, len(arkPricing))
	for model, price := range arkPricing {
		out[model] = price
	}
	return out
}

// ResolvePricing returns the price for a model and how it was found.
func ResolvePricing(model string) (ModelPricing, PricingSource) {
	raw := strings.ToLower(strings.TrimSpace(model))
	if _, ok := endpointToModel[raw]; ok {
		// An endpoint id names a deployment we mapped by hand: that is exact.
		mapped := normalizeModelName(raw)
		pricingMu.RLock()
		price, found := arkPricing[mapped]
		pricingMu.RUnlock()
		if found {
			return price, PricingExact
		}
		return ModelPricing{}, PricingUnknown
	}

	pricingMu.RLock()
	price, exact := arkPricing[raw]
	pricingMu.RUnlock()
	if exact {
		return price, PricingExact
	}

	guessed, known := guessModelFamily(raw)
	if !known {
		return ModelPricing{}, PricingUnknown
	}
	pricingMu.RLock()
	price, found := arkPricing[guessed]
	pricingMu.RUnlock()
	if !found {
		return ModelPricing{}, PricingUnknown
	}
	return price, PricingHeuristic
}

// endpointToModel maps a deployment id to the model it actually serves.
//
// **This is console state, not something the name tells you.** Verified against
// the API on 2026-09-18 with `go run ./cmd/ark-endpoint-probe`, which asks each
// endpoint what model it is:
//
//	ep-…4651-pffhf (REVIEW_REFINE)  → doubao-seed-2-1-pro-260628
//	ep-…4818-8kdfr (DAILY_READ)     → doubao-seed-2-1-pro-260628
//	ep-…4912-wtjw9 (TOPIC_CARD)     → doubao-seed-2-1-pro-260628
//	ep-…5333-prddb (HIT_MATCH)      → doubao-seed-2-1-turbo-260628
//	ep-…5423-xg4pd (DRILL_JUDGE)    → doubao-seed-2-1-turbo-260628
//	ep-…5520-d9d8n (TEXT_DEGRADE)   → doubao-seed-2-1-turbo-260628
//
// The previous revision of this table claimed all six were doubao-mini-32k,
// which priced every LLM row off a model nobody was running. Re-run the probe
// after changing a deployment in the console; the map is only true until
// somebody re-points an endpoint.
var endpointToModel = map[string]string{
	// Dev/POC endpoints (project default).
	"ep-20260830204651-pffhf": "doubao-seed-2-1-pro-260628",   // ARK_EP_REVIEW_REFINE
	"ep-20260830204818-8kdfr": "doubao-seed-2-1-pro-260628",   // ARK_EP_DAILY_READ
	"ep-20260830204912-wtjw9": "doubao-seed-2-1-pro-260628",   // ARK_EP_TOPIC_CARD
	"ep-20260830205333-prddb": "doubao-seed-2-1-turbo-260628", // ARK_EP_HIT_MATCH
	"ep-20260830205423-xg4pd": "doubao-seed-2-1-turbo-260628", // ARK_EP_DRILL_JUDGE
	"ep-20260830205520-d9d8n": "doubao-seed-2-1-turbo-260628", // ARK_EP_TEXT_DEGRADE

	// Prod endpoints (project FluentWork-Prod). Not probed yet: these were
	// never called from this machine, so the mapping is the assumption that
	// they mirror dev. Verify with the probe before trusting a prod bill.
	"ep-20260830211617-26d79": "doubao-seed-2-1-pro-260628",   // ARK_EP_REVIEW_REFINE (Prod)
	"ep-20260830211650-vkdj2": "doubao-seed-2-1-pro-260628",   // ARK_EP_DAILY_READ (Prod)
	"ep-20260830211715-q79x9": "doubao-seed-2-1-pro-260628",   // ARK_EP_TOPIC_CARD (Prod)
	"ep-20260830211747-vwtrb": "doubao-seed-2-1-turbo-260628", // ARK_EP_HIT_MATCH (Prod)
	"ep-20260830211815-pmkg6": "doubao-seed-2-1-turbo-260628", // ARK_EP_DRILL_JUDGE (Prod)
	"ep-20260830211850-pf2ts": "doubao-seed-2-1-turbo-260628", // ARK_EP_TEXT_DEGRADE (Prod)
}

// CalculateCost 计算 LLM 调用费用（单位：分）。
//
// A model no rule recognises costs 0 and is counted, rather than being priced as
// whatever family its name resembles: the old fallback billed an unknown model at
// doubao-pro-32k rates, which is a number that looks authoritative and is wrong
// by a factor. Zero is visibly unknown; a plausible wrong number is not.
func CalculateCost(model string, promptTokens, outputTokens int) int {
	if promptTokens <= 0 && outputTokens <= 0 {
		return 0
	}
	pricing, source := ResolvePricing(model)
	switch source {
	case PricingUnknown:
		incUnpricedModel(strings.TrimSpace(model))
		return 0
	case PricingHeuristic:
		incHeuristicPricedModel(strings.TrimSpace(model))
	}

	inputCostFen := (promptTokens * pricing.InputPricePerKToken) / 1000
	outputCostFen := (outputTokens * pricing.OutputPricePerKToken) / 1000

	totalCost := inputCostFen + outputCostFen
	if totalCost == 0 {
		// Usage that rounds to nothing is still usage: a 1-fen floor keeps a
		// very short call from recording as free.
		return 1
	}
	return totalCost
}

// guessModelFamily infers the price family from a model name. The second return
// value is false when nothing matched — the caller then records 0 rather than
// inventing a family.
func guessModelFamily(model string) (string, bool) {
	model = strings.TrimSpace(strings.ToLower(model))
	if strings.Contains(model, "mini") {
		return "doubao-mini-32k", true
	}
	if strings.Contains(model, "lite") {
		return "doubao-lite-32k", true
	}
	if strings.Contains(model, "character") {
		return "doubao-character-32k", true
	}
	if strings.Contains(model, "pro") {
		if strings.Contains(model, "4k") {
			return "doubao-pro-4k", true
		}
		if strings.Contains(model, "128k") {
			return "doubao-pro-128k", true
		}
		if strings.Contains(model, "256k") {
			return "doubao-pro-256k", true
		}
		if strings.Contains(model, "search") {
			return "doubao-pro-search", true
		}
		return "doubao-pro-32k", true
	}
	return "", false
}

// normalizeModelName keeps the historic shape for callers that only want a name.
func normalizeModelName(model string) string {
	model = strings.TrimSpace(strings.ToLower(model))
	if mapped, ok := endpointToModel[model]; ok {
		return mapped
	}
	if guessed, ok := guessModelFamily(model); ok {
		return guessed
	}
	return model
}
