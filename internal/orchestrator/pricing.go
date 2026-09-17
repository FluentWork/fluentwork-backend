package orchestrator

import "strings"

// ModelPricing 定义模型的输入/输出 token 单价（单位：分/千 tokens）
type ModelPricing struct {
	InputPricePerKToken  int // 输入 token 单价（分/1K tokens）
	OutputPricePerKToken int // 输出 token 单价（分/1K tokens）
}

// 火山 Ark 模型定价表（2026-09 数据）
// 参考：https://www.volcengine.com/docs/82379/1099320
var arkPricing = map[string]ModelPricing{
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

// Endpoint ID 到模型映射表（基于 configs/volc.env.example 实际使用的 endpoints）
// 注：根据文档 docs/24_B8_followup 和 meta/docs/30_技术方案/47_API契约冻结，
// 实际使用的是 Ark Mini (doubao-mini-32k)，定价 0.3元/M input, 0.6元/M output
var endpointToModel = map[string]string{
	// Dev/POC Endpoints (项目 default) - 使用 Ark Mini
	"ep-20260830204651-pffhf": "doubao-mini-32k",   // ARK_EP_REVIEW_REFINE
	"ep-20260830204818-8kdfr": "doubao-mini-32k",   // ARK_EP_DAILY_READ
	"ep-20260830204912-wtjw9": "doubao-mini-32k",   // ARK_EP_TOPIC_CARD
	"ep-20260830205333-prddb": "doubao-mini-32k",   // ARK_EP_HIT_MATCH
	"ep-20260830205423-xg4pd": "doubao-mini-32k",   // ARK_EP_DRILL_JUDGE
	"ep-20260830205520-d9d8n": "doubao-mini-32k",   // ARK_EP_TEXT_DEGRADE

	// Prod Endpoints (项目 FluentWork-Prod) - 使用 Ark Mini
	"ep-20260830211617-26d79": "doubao-mini-32k",   // ARK_EP_REVIEW_REFINE (Prod)
	"ep-20260830211650-vkdj2": "doubao-mini-32k",   // ARK_EP_DAILY_READ (Prod)
	"ep-20260830211715-q79x9": "doubao-mini-32k",   // ARK_EP_TOPIC_CARD (Prod)
	"ep-20260830211747-vwtrb": "doubao-mini-32k",   // ARK_EP_HIT_MATCH (Prod)
	"ep-20260830211815-pmkg6": "doubao-mini-32k",   // ARK_EP_DRILL_JUDGE (Prod)
	"ep-20260830211850-pf2ts": "doubao-mini-32k",   // ARK_EP_TEXT_DEGRADE (Prod)
}

// CalculateCost 计算 LLM 调用费用（单位：分）
func CalculateCost(model string, promptTokens, outputTokens int) int {
	if promptTokens <= 0 && outputTokens <= 0 {
		return 0
	}

	model = normalizeModelName(model)
	pricing, ok := arkPricing[model]
	if !ok {
		pricing = arkPricing["doubao-pro-32k"]
	}

	inputCostFen := (promptTokens * pricing.InputPricePerKToken) / 1000
	outputCostFen := (outputTokens * pricing.OutputPricePerKToken) / 1000

	totalCost := inputCostFen + outputCostFen
	if totalCost == 0 && (promptTokens > 0 || outputTokens > 0) {
		return 1
	}

	return totalCost
}

// normalizeModelName 标准化模型名称，处理 endpoint ID 到模型名的映射
func normalizeModelName(model string) string {
	model = strings.TrimSpace(strings.ToLower(model))

	// 1. 精确匹配 endpoint ID
	if mapped, ok := endpointToModel[model]; ok {
		return mapped
	}

	// 2. 通用模型名称匹配（用于 Ark 直接返回的 model 字段）
	if strings.Contains(model, "mini") {
		return "doubao-mini-32k"
	}
	if strings.Contains(model, "lite") {
		return "doubao-lite-32k"
	}
	if strings.Contains(model, "character") {
		return "doubao-character-32k"
	}
	if strings.Contains(model, "pro") {
		if strings.Contains(model, "4k") {
			return "doubao-pro-4k"
		}
		if strings.Contains(model, "128k") {
			return "doubao-pro-128k"
		}
		if strings.Contains(model, "256k") {
			return "doubao-pro-256k"
		}
		if strings.Contains(model, "search") {
			return "doubao-pro-search"
		}
		return "doubao-pro-32k"
	}

	// 3. 默认回退到 mini（最经济的模型）
	return "doubao-mini-32k"
}
