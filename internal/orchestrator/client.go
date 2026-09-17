package orchestrator

import (
	"context"

	"github.com/FluentWork/fluentwork-backend/internal/config"
)

// CompletionRequest 统一的 LLM 请求
type CompletionRequest struct {
	Prompt         string
	MaxTokens      int
	Temperature    float64
	SystemPrompt   string
	ResponseFormat string // "json_object" or ""
	Operation      string // routes the call to an endpoint and labels the cost row
	// UserID attributes the cost row. Optional: some calls (a shared warm-up, a
	// batch job) belong to nobody in particular.
	UserID string
}

// CompletionResponse 统一的 LLM 响应
type CompletionResponse struct {
	Content string
	// FinishReason 是供应商给出的停止原因（"stop" / "length" ...）。"length"
	// 表示输出被 max_tokens 截断，调用方据此把 JSON 解不开判为截断而非格式错。
	FinishReason string
	PromptTokens int
	OutputTokens int
	TotalTokens  int
	Model        string
	LatencyMS    int64
}

// Client LLM 客户端接口（支持 Ark / OpenAI / Mock）
type Client interface {
	Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error)
}

// CostWriter 成本记账接口（隔离 aicost 包依赖）
type CostWriter interface {
	Write(ctx context.Context, log CostLog) error
}

// CostLog 成本记录
type CostLog struct {
	UserID       string
	SessionID    string
	Operation    string
	PromptTokens int
	OutputTokens int
	TotalTokens  int
	Model        string
	LatencyMS    int64
}

var defaultClient Client

// NewClient 根据 config 返回配置好的 Client
// 使用单例模式，所有调用共享同一个 HTTP 连接池
func NewClient(cfg config.Config, costWriter CostWriter) Client {
	if defaultClient == nil {
		defaultClient = NewArkClient(cfg, costWriter)
	}
	return defaultClient
}
