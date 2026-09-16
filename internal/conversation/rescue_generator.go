// Package conversation holds the B8 stuck-rescue language logic: whether the
// user's utterance came to a stop mid-thought, and what to offer them instead.
package conversation

import (
	"context"
	"fmt"
)

// RescueLevel 救援层级
type RescueLevel int

const (
	// RescueSkeleton 句首骨架：把一句话的开头交给用户
	RescueSkeleton RescueLevel = 1
	// RescueHint 中文意图提示：点出组织答案的思路
	RescueHint RescueLevel = 2
	// RescueComplete 完整表达：一句可以直接说或改写的范例
	RescueComplete RescueLevel = 3
)

// ConversationContext 对话上下文
//
// 名字里的 conversation 与包名重复（revive 的 stutter 规则会对这一条报警，
// 本仓保留不改），两个理由：一是 `conversation.Context` 会与同包签名里的
// `context.Context` 抢读者的注意力；二是这个名字已经写进跨仓的
// `meta/docs/40_研发流程与协作/78_PRD_V1.6新增功能实现方案`，改名的收益抵不上
// 让计划文档与代码对不上的代价。
type ConversationContext struct {
	LastAIMessage   string // AI 最后一条消息
	ScenarioContext string // 场景上下文（用户素材）
	UserRole        string // 用户角色（如 "Backend Engineer"）
	RecentTurns     []Turn // 最近几轮对话
}

// Turn 对话话轮
type Turn struct {
	Speaker string // "user" | "ai"
	Content string
}

// LLMClient LLM 客户端接口
type LLMClient interface {
	Complete(ctx context.Context, prompt string, options CompletionOptions) (string, error)
}

// CompletionOptions LLM 补全选项
type CompletionOptions struct {
	MaxTokens   int
	Model       string
	Temperature float64
}

// RescueGenerator 救援内容生成器
type RescueGenerator struct {
	llmClient LLMClient
}

// NewRescueGenerator 创建救援生成器
func NewRescueGenerator(llmClient LLMClient) *RescueGenerator {
	return &RescueGenerator{
		llmClient: llmClient,
	}
}

// GenerateRescue 生成对应层级的救援内容
func (g *RescueGenerator) GenerateRescue(ctx context.Context, level RescueLevel, conversationContext ConversationContext) (string, error) {
	switch level {
	case RescueSkeleton:
		return g.generateSkeleton(ctx, conversationContext)
	case RescueHint:
		return g.generateHint(ctx, conversationContext)
	case RescueComplete:
		return g.generateComplete(ctx, conversationContext)
	default:
		return "", fmt.Errorf("unknown rescue level: %d", level)
	}
}

// generateSkeleton 生成句首骨架（Layer 1）
func (g *RescueGenerator) generateSkeleton(ctx context.Context, conv ConversationContext) (string, error) {
	prompt := fmt.Sprintf(`You are helping a non-native English speaker who got stuck in conversation.

Last AI question: %s
Scenario context: %s
User's role: %s

Generate a sentence starter (skeleton) that helps the user continue. The skeleton should:
- Start the sentence structure for the user
- End with "..." to indicate the user should complete it
- Be natural and directly address the AI's question
- Be simple and clear

Examples of good skeletons:
- "I think the main risk is..."
- "The key difference between X and Y is..."
- "Let me explain why..."
- "The solution would be..."

Return ONLY the skeleton (one line), no explanation.`,
		conv.LastAIMessage,
		conv.ScenarioContext,
		conv.UserRole,
	)

	result, err := g.llmClient.Complete(ctx, prompt, CompletionOptions{
		MaxTokens:   30,
		Model:       "doubao-lite-32k", // 小模型足够
		Temperature: 0.7,
	})
	if err != nil {
		return "", fmt.Errorf("failed to generate skeleton: %w", err)
	}

	return result, nil
}

// generateHint 生成中文意图提示（Layer 2）
func (g *RescueGenerator) generateHint(ctx context.Context, conv ConversationContext) (string, error) {
	prompt := fmt.Sprintf(`用户在英语对话中卡壳了，需要用中文给一个思路提示。

上一个 AI 问题：%s
场景上下文：%s
用户角色：%s

用一句简短的中文（10-20字）提示用户应该如何组织回答（思路提示，不给具体英文）。

好的提示例子：
- "先说结论，再说原因"
- "可以举个具体例子"
- "说说你的顾虑是什么"
- "对比一下两个方案"

只返回提示语，不要解释或多余的话。`,
		conv.LastAIMessage,
		conv.ScenarioContext,
		conv.UserRole,
	)

	result, err := g.llmClient.Complete(ctx, prompt, CompletionOptions{
		MaxTokens:   40,
		Model:       "doubao-lite-32k",
		Temperature: 0.7,
	})
	if err != nil {
		return "", fmt.Errorf("failed to generate hint: %w", err)
	}

	return result, nil
}

// generateComplete 生成完整表达（Layer 3）
func (g *RescueGenerator) generateComplete(ctx context.Context, conv ConversationContext) (string, error) {
	// 构建最近对话历史
	recentHistory := ""
	if len(conv.RecentTurns) > 0 {
		recentHistory = "\nRecent conversation:\n"
		for _, turn := range conv.RecentTurns {
			recentHistory += fmt.Sprintf("%s: %s\n", turn.Speaker, turn.Content)
		}
	}

	prompt := fmt.Sprintf(`Generate a complete, natural English response for a programmer in this conversation.

Last AI question: %s
Scenario context: %s
User's role: %s
%s

Generate ONE complete sentence that directly answers the AI's question. Requirements:
- Keep it simple and professional
- Use programmer's terminology naturally
- Be specific enough to continue the conversation
- 1-2 sentences maximum

Return ONLY the response, no explanation.`,
		conv.LastAIMessage,
		conv.ScenarioContext,
		conv.UserRole,
		recentHistory,
	)

	result, err := g.llmClient.Complete(ctx, prompt, CompletionOptions{
		MaxTokens:   80,
		Model:       "doubao-pro-32k", // 完整表达用大模型保证质量
		Temperature: 0.7,
	})
	if err != nil {
		return "", fmt.Errorf("failed to generate complete response: %w", err)
	}

	return result, nil
}
