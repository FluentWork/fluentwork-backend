package orchestrator

import (
	"context"

	"github.com/FluentWork/fluentwork-backend/internal/aicost"
)

// AICostWriterAdapter 将 aicost.Service 适配为 orchestrator.CostWriter 接口
type AICostWriterAdapter struct {
	service *aicost.Service
}

// NewAICostWriterAdapter 创建 aicost.Service 到 CostWriter 的适配器
func NewAICostWriterAdapter(service *aicost.Service) *AICostWriterAdapter {
	return &AICostWriterAdapter{service: service}
}

// Write 实现 CostWriter 接口
func (a *AICostWriterAdapter) Write(ctx context.Context, log CostLog) error {
	if a.service == nil {
		return nil // graceful: 未配置 service 时跳过记录
	}

	req := aicost.RecordRequest{
		UserID:    log.UserID,
		TaskType:  log.Operation,
		Model:     log.Model,
		TokensIn:  log.PromptTokens,
		TokensOut: log.OutputTokens,
		AudioSec:  0,
		CostFen:   CalculateCost(log.Model, log.PromptTokens, log.OutputTokens),
	}

	_, err := a.service.Record(ctx, req)
	return err
}
