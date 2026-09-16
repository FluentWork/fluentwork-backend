package conversation

import (
	"testing"
)

func TestIncompleteDetector_IsIncomplete(t *testing.T) {
	detector := NewIncompleteDetector()

	tests := []struct {
		name     string
		input    string
		expected bool
	}{
		// 连接词结尾（语义截断）
		{
			name:     "ends with but",
			input:    "I think the solution is good but",
			expected: true,
		},
		{
			name:     "ends with and",
			input:    "We need to refactor the code and",
			expected: true,
		},
		{
			name:     "ends with because",
			input:    "The test failed because",
			expected: true,
		},
		{
			name:     "ends with so",
			input:    "The performance is slow so",
			expected: true,
		},
		{
			name:     "ends with if",
			input:    "We can deploy tomorrow if",
			expected: true,
		},

		// 省略号结尾
		{
			name:     "ends with ellipsis",
			input:    "I'm not sure...",
			expected: true,
		},
		{
			name:     "ends with dash",
			input:    "The main issue is--",
			expected: true,
		},

		// 缺谓语（少于 3 词）
		{
			name:     "two words no verb",
			input:    "the solution",
			expected: true,
		},
		{
			name:     "one word",
			input:    "okay",
			expected: false, // 单词视为完整（简短回应）
		},

		// 完整句子（不触发）
		{
			name:     "complete sentence",
			input:    "I think the main risk is performance.",
			expected: false,
		},
		{
			name:     "complete with but in middle",
			input:    "It works but has some bugs.",
			expected: false,
		},
		{
			name:     "short but has verb",
			input:    "It is",
			expected: false,
		},
		{
			name:     "normal response",
			input:    "Yes, we can do that tomorrow.",
			expected: false,
		},

		// 边界情况
		{
			name:     "empty string",
			input:    "",
			expected: false,
		},
		{
			name:     "only spaces",
			input:    "   ",
			expected: false,
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			got := detector.IsIncomplete(tt.input)
			if got != tt.expected {
				t.Errorf("IsIncomplete(%q) = %v, want %v", tt.input, got, tt.expected)
			}
		})
	}
}
