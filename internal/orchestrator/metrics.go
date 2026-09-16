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
}

var globalMetrics = &Metrics{}

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

// Reset 重置所有计数器（仅用于测试）
func (m *Metrics) Reset() {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.CostWriteFailuresTotal = 0
	m.CompletionsTotal = 0
	m.CompletionErrorsTotal = 0
}
