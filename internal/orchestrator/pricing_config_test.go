package orchestrator

import (
	"os"
	"path/filepath"
	"testing"
)

func restoreDefaultPricing(t *testing.T) {
	t.Helper()
	if err := ConfigurePricing(defaultArkPricing()); err != nil {
		t.Fatalf("restore default pricing: %v", err)
	}
}

// doc 79/P2-2：费率是运维数据。文件能替换整张表，让"以账单为准"落成改数据而不是改代码。
func TestLoadPricingFile_ReplacesTable(t *testing.T) {
	defer restoreDefaultPricing(t)
	path := filepath.Join(t.TempDir(), "pricing.json")
	body := `{"doubao-mini-32k": {"input_price_per_k_token": 1, "output_price_per_k_token": 2}}`
	if err := os.WriteFile(path, []byte(body), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}

	if err := LoadPricingFile(path); err != nil {
		t.Fatalf("LoadPricingFile: %v", err)
	}
	table := PricingTable()
	if len(table) != 1 || table["doubao-mini-32k"].InputPricePerKToken != 1 {
		t.Fatalf("table = %+v", table)
	}
	// 1000 in + 500 out at 1/2 分 per 1K = 1 + 1 = 2 分.
	if got := CalculateCost("doubao-mini-32k", 1000, 500); got != 2 {
		t.Fatalf("cost = %d, want the file's rates", got)
	}
}

// 文件坏了要启动即失败：静默沿用旧费率，等于用没人选过的数字记账。
func TestLoadPricingFile_FailsLoudly(t *testing.T) {
	defer restoreDefaultPricing(t)
	dir := t.TempDir()

	missing := filepath.Join(dir, "nope.json")
	if err := LoadPricingFile(missing); err == nil {
		t.Fatal("a missing file must fail")
	}

	malformed := filepath.Join(dir, "bad.json")
	if err := os.WriteFile(malformed, []byte("{not json"), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := LoadPricingFile(malformed); err == nil {
		t.Fatal("malformed JSON must fail")
	}

	negative := filepath.Join(dir, "negative.json")
	if err := os.WriteFile(negative, []byte(`{"m": {"input_price_per_k_token": -1}}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := LoadPricingFile(negative); err == nil {
		t.Fatal("negative prices must fail")
	}

	empty := filepath.Join(dir, "empty.json")
	if err := os.WriteFile(empty, []byte(`{}`), 0o600); err != nil {
		t.Fatalf("write: %v", err)
	}
	if err := LoadPricingFile(empty); err == nil {
		t.Fatal("an empty table must fail")
	}
}

// ConfigurePricing 是全量替换：不做合并，避免"一半来自文件、一半来自默认"。
func TestConfigurePricing_RejectsBadTables(t *testing.T) {
	defer restoreDefaultPricing(t)
	if err := ConfigurePricing(nil); err == nil {
		t.Fatal("nil table must be refused")
	}
	if err := ConfigurePricing(map[string]ModelPricing{"": {InputPricePerKToken: 1}}); err == nil {
		t.Fatal("empty model name must be refused")
	}
	if err := ConfigurePricing(map[string]ModelPricing{"m": {OutputPricePerKToken: -1}}); err == nil {
		t.Fatal("negative price must be refused")
	}
}

func TestResolvePricing_Sources(t *testing.T) {
	defer restoreDefaultPricing(t)

	if _, source := ResolvePricing("doubao-mini-32k"); source != PricingExact {
		t.Fatalf("exact table hit = %q", source)
	}
	if _, source := ResolvePricing("doubao-seed-2-1-pro-260628"); source != PricingHeuristic {
		t.Fatalf("family guess = %q, want heuristic", source)
	}
	if _, source := ResolvePricing("kimi-k2-0711"); source != PricingUnknown {
		t.Fatalf("unrecognised model = %q, want unknown", source)
	}
	// An endpoint id resolves to whatever model the probe found; whether that is
	// "exact" depends on the model having a price entry. Today the deployed seed
	// models have none, so the endpoint is unpriced rather than mispriced.
	if _, source := ResolvePricing("ep-20260830204651-pffhf"); source != PricingUnknown {
		t.Fatalf("endpoint id without a price entry = %q, want unknown", source)
	}
	// With the deployed model priced — the state ARK_PRICING_FILE produces — the
	// same endpoint becomes exact.
	if err := ConfigurePricing(map[string]ModelPricing{
		"doubao-seed-2-1-pro-260628": {InputPricePerKToken: 3, OutputPricePerKToken: 6},
	}); err != nil {
		t.Fatalf("ConfigurePricing: %v", err)
	}
	price, source := ResolvePricing("ep-20260830204651-pffhf")
	if source != PricingExact || price.InputPricePerKToken != 3 {
		t.Fatalf("endpoint id with a price entry = %q %+v", source, price)
	}
}

// 猜出来的价格和认不出来的模型都要在指标里看得见——否则运维无从知道该往定价表里补什么。
func TestCalculateCost_CountsGuessesAndGaps(t *testing.T) {
	defer restoreDefaultPricing(t)
	globalMetrics.Reset()
	t.Cleanup(globalMetrics.Reset)

	if got := CalculateCost("kimi-k2-0711", 1000, 500); got != 0 {
		t.Fatalf("unpriced cost = %d, want 0", got)
	}
	if got := CalculateCost("doubao-seed-2-1-pro-260628", 1000, 500); got == 0 {
		t.Fatal("a family guess is still priced")
	}

	metrics := GetMetrics()
	metrics.mu.Lock()
	defer metrics.mu.Unlock()
	if metrics.UnpricedModels["kimi-k2-0711"] != 1 {
		t.Fatalf("unpriced = %+v", metrics.UnpricedModels)
	}
	if metrics.HeuristicPricedModels["doubao-seed-2-1-pro-260628"] != 1 {
		t.Fatalf("heuristic = %+v", metrics.HeuristicPricedModels)
	}
}
