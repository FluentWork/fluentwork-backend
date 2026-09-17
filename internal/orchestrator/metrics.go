package orchestrator

import (
	"sync"
)

// Metrics 暴露 orchestrator 包的可观测性指标
type Metrics struct {
	mu                     sync.Mutex
	CostWriteFailuresTotal int64
	CompletionsTotal       int64
	CompletionErrorsTotal  int64
	// UnpricedModels counts calls whose model matched no pricing rule. They are
	// recorded at cost_fen = 0; the label names the model so ops can add it to
	// the pricing file (doc 79).
	UnpricedModels map[string]int64
	// HeuristicPricedModels counts calls priced by guessing the model's family
	// from its name — a real price, but not one anybody typed for that model.
	HeuristicPricedModels map[string]int64
}

var globalMetrics = &Metrics{
	UnpricedModels:        map[string]int64{},
	HeuristicPricedModels: map[string]int64{},
}

// GetMetrics 返回全局 metrics 实例（用于 Prometheus 或监控系统采集）
func GetMetrics() *Metrics {
	return globalMetrics
}

func (m *Metrics) incCostWriteFailures() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.CostWriteFailuresTotal++
}

func (m *Metrics) incCompletions() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.CompletionsTotal++
}

func (m *Metrics) incCompletionErrors() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.CompletionErrorsTotal++
}

func (m *Metrics) incUnpriced(model string) {
	if model == "" {
		model = "unknown"
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.UnpricedModels == nil {
		m.UnpricedModels = map[string]int64{}
	}
	m.UnpricedModels[model]++
}

func (m *Metrics) incHeuristicPriced(model string) {
	if model == "" {
		model = "unknown"
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	if m.HeuristicPricedModels == nil {
		m.HeuristicPricedModels = map[string]int64{}
	}
	m.HeuristicPricedModels[model]++
}

func incUnpricedModel(model string)        { globalMetrics.incUnpriced(model) }
func incHeuristicPricedModel(model string) { globalMetrics.incHeuristicPriced(model) }

// Reset 重置所有计数器（仅用于测试）
func (m *Metrics) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.CostWriteFailuresTotal = 0
	m.CompletionsTotal = 0
	m.CompletionErrorsTotal = 0
	m.UnpricedModels = map[string]int64{}
	m.HeuristicPricedModels = map[string]int64{}
}
