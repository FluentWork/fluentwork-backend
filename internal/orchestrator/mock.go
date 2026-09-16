package orchestrator

import "context"

// MockClient 测试用 LLM 客户端
type MockClient struct {
	Response CompletionResponse
	Err      error
	// 可选：记录调用历史
	Calls []CompletionRequest
}

// Complete 实现 Client 接口，返回预设的响应或错误
func (m *MockClient) Complete(ctx context.Context, req CompletionRequest) (CompletionResponse, error) {
	if m.Calls != nil {
		m.Calls = append(m.Calls, req)
	}
	return m.Response, m.Err
}

// NewMockClient 创建一个返回固定响应的 mock 客户端
func NewMockClient(content string) *MockClient {
	return &MockClient{
		Response: CompletionResponse{
			Content:      content,
			PromptTokens: 100,
			OutputTokens: 50,
			TotalTokens:  150,
			Model:        "mock-model",
			LatencyMS:    10,
		},
		Calls: []CompletionRequest{},
	}
}

// NewMockClientWithError 创建一个返回错误的 mock 客户端
func NewMockClientWithError(err error) *MockClient {
	return &MockClient{
		Err: err,
	}
}
