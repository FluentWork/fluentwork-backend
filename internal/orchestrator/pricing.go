package orchestrator

import (
	"encoding/json"
	"fmt"
	"math"
	"os"
	"strings"
	"sync"
)

// ModelPricing is a model's price in 微元 per million tokens (10^-6 CNY / 1M).
//
// Microunits rather than 分, because current models cost far less than one 分 per
// call: 0.8 CNY/1M means a 1000+500 token call is 0.12 分, and integer fen cannot
// say that — every call rounded up to the 1-分 floor and the ledger became
// "calls × 1 分", a number that looks like money and is not.
//
// Per million, because that is the unit vendors publish: an operator copies the
// number off the pricing page or the bill with no arithmetic in between.
type ModelPricing struct {
	InputMicroYuanPerMillion  int64
	OutputMicroYuanPerMillion int64
}

// defaultArkPricing is the built-in 火山 Ark price table.
//
// It is a *default*, not the source of truth: P2-2 settled that vendor billing
// decides the rates, and doc 79 asked for the table to be replaceable without a
// deploy. LoadPricingFile does that — ops corrects the numbers, not the code.
//
// Only entries with a stated source live here. The rest of the old table was
// dropped deliberately: its numbers were 分 per 1K and contradicted their own
// comment by 100x (3 分/1K for a model documented at 0.3 CNY/1M), with no source
// recorded for any of them. A model with no entry is recorded at 0 and counted
// as unpriced — the honest state until a bill supplies a rate.
func defaultArkPricing() map[string]ModelPricing {
	return map[string]ModelPricing{
		// 0.3 CNY/1M in, 0.6 CNY/1M out.
		"doubao-mini-32k": {InputMicroYuanPerMillion: 300_000, OutputMicroYuanPerMillion: 600_000},
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
		// by leaving the model out (it then records 0 and is counted as
		// unpriced), not by writing a zero that prices every call at the floor.
		if price.InputMicroYuanPerMillion <= 0 || price.OutputMicroYuanPerMillion <= 0 {
			return fmt.Errorf("pricing for %q must be positive; omit the model instead of pricing it at 0", model)
		}
		cleaned[name] = price
	}
	pricingMu.Lock()
	arkPricing = cleaned
	pricingMu.Unlock()
	return nil
}

// pricingFileEntry is the on-disk shape of one model's prices, in the unit the
// vendor publishes. The conversion to microunits happens once, on load.
type pricingFileEntry struct {
	InputCNYPerMillion  float64 `json:"input_cny_per_million"`
	OutputCNYPerMillion float64 `json:"output_cny_per_million"`
}

// LoadPricingFile reads an override table from JSON, in the unit the vendor
// publishes:
//
//	{"doubao-seed-2-1-pro-260628": {"input_cny_per_million": 0.8, "output_cny_per_million": 2.0}}
//
// The caller fails startup on error on purpose: a pricing file that does not
// parse means somebody intended to change the rates, and quietly running on the
// defaults would bill at numbers nobody chose.
func LoadPricingFile(path string) error {
	raw, err := os.ReadFile(path)
	if err != nil {
		return fmt.Errorf("read pricing file: %w", err)
	}
	var parsed map[string]json.RawMessage
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return fmt.Errorf("parse pricing file: %w", err)
	}
	entries := make(map[string]pricingFileEntry, len(parsed))
	for key, value := range parsed {
		if strings.HasPrefix(strings.TrimSpace(key), "_") {
			continue
		}
		var entry pricingFileEntry
		if err := json.Unmarshal(value, &entry); err != nil {
			return fmt.Errorf("parse pricing entry %q: %w", key, err)
		}
		entries[key] = entry
	}
	if len(entries) == 0 {
		return fmt.Errorf("pricing file has no models (only comment keys)")
	}
	// A model that ends up without a rate shows in UnpricedModels rather than
	// being silently mispriced.
	prices := make(map[string]ModelPricing, len(entries))
	for model, entry := range entries {
		prices[model] = ModelPricing{
			InputMicroYuanPerMillion:  cnyToMicroYuanPerMillion(entry.InputCNYPerMillion),
			OutputMicroYuanPerMillion: cnyToMicroYuanPerMillion(entry.OutputCNYPerMillion),
		}
	}
	return ConfigurePricing(prices)
}

// cnyToMicroYuanPerMillion converts the published unit to the internal one.
// Rounding at 10^-6 CNY is far below any rate a vendor publishes.
func cnyToMicroYuanPerMillion(cny float64) int64 {
	return int64(math.Round(cny * 1_000_000))
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

// CalculateCostMicroYuan prices one call, in 微元 (10^-6 CNY).
//
// A model no rule recognises costs 0 and is counted, rather than being priced as
// whatever family its name resembles: a plausible wrong number is worse than a
// visible gap.
func CalculateCostMicroYuan(model string, promptTokens, outputTokens int) int64 {
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

	// Integer math on microunits: tokens × (微元/1M) / 1M stays exact for any
	// rate a vendor publishes.
	total := (int64(promptTokens)*pricing.InputMicroYuanPerMillion +
		int64(outputTokens)*pricing.OutputMicroYuanPerMillion) / 1_000_000
	if total == 0 {
		// Usage that rounds to nothing is still usage: a 1-微元 floor (10^-6 CNY)
		// keeps a very short call from recording as free.
		return 1
	}
	return total
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
