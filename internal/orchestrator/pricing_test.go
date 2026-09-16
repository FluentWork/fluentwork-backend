package orchestrator

import "testing"

func TestCalculateCost(t *testing.T) {
	tests := []struct {
		name          string
		model         string
		promptTokens  int
		outputTokens  int
		expectedCost  int
	}{
		{
			name:         "doubao-pro-32k standard usage",
			model:        "doubao-pro-32k",
			promptTokens: 1000,
			outputTokens: 500,
			expectedCost: 9, // (1000*5)/1000 + (500*9)/1000 = 5 + 4 = 9
		},
		{
			name:         "endpoint ID maps to doubao-pro-32k",
			model:        "ep-20260830204651-pffhf",
			promptTokens: 2000,
			outputTokens: 1000,
			expectedCost: 19, // (2000*5)/1000 + (1000*9)/1000 = 10 + 9 = 19
		},
		{
			name:         "doubao-lite-32k cheaper pricing",
			model:        "doubao-lite-32k",
			promptTokens: 1000,
			outputTokens: 500,
			expectedCost: 6, // (1000*3)/1000 + (500*6)/1000 = 3 + 3 = 6
		},
		{
			name:         "doubao-pro-4k higher pricing",
			model:        "doubao-pro-4k",
			promptTokens: 1000,
			outputTokens: 500,
			expectedCost: 12, // (1000*8)/1000 + (500*8)/1000 = 8 + 4 = 12
		},
		{
			name:         "zero tokens returns zero cost",
			model:        "doubao-pro-32k",
			promptTokens: 0,
			outputTokens: 0,
			expectedCost: 0,
		},
		{
			name:         "small usage rounds up to 1 fen minimum",
			model:        "doubao-pro-32k",
			promptTokens: 10,
			outputTokens: 10,
			expectedCost: 1, // Too small to round to 0, returns 1
		},
		{
			name:         "unknown model defaults to doubao-pro-32k",
			model:        "unknown-model",
			promptTokens: 1000,
			outputTokens: 500,
			expectedCost: 9,
		},
		{
			name:         "doubao-pro-256k premium pricing",
			model:        "doubao-pro-256k",
			promptTokens: 1000,
			outputTokens: 1000,
			expectedCost: 80, // (1000*20)/1000 + (1000*60)/1000 = 20 + 60 = 80
		},
	}

	for _, tt := range tests {
		t.Run(tt.name, func(t *testing.T) {
			cost := CalculateCost(tt.model, tt.promptTokens, tt.outputTokens)
			if cost != tt.expectedCost {
				t.Errorf("CalculateCost(%q, %d, %d) = %d; want %d",
					tt.model, tt.promptTokens, tt.outputTokens, cost, tt.expectedCost)
			}
		})
	}
}

func TestNormalizeModelName(t *testing.T) {
	tests := []struct {
		input    string
		expected string
	}{
		{"ep-20260830204651-pffhf", "doubao-pro-32k"},
		{"EP-20260830204651-PFFHF", "doubao-pro-32k"},
		{"doubao-pro-32k", "doubao-pro-32k"},
		{"Doubao-Pro-32K", "doubao-pro-32k"},
		{"doubao-lite-128k", "doubao-lite-32k"},
		{"doubao-character-4k", "doubao-character-32k"},
		{"doubao-pro-4k", "doubao-pro-4k"},
		{"doubao-pro-128k", "doubao-pro-128k"},
		{"doubao-pro-256k", "doubao-pro-256k"},
		{"doubao-pro-search", "doubao-pro-search"},
		{"unknown", "doubao-pro-32k"},
		{"", "doubao-pro-32k"},
	}

	for _, tt := range tests {
		t.Run(tt.input, func(t *testing.T) {
			result := normalizeModelName(tt.input)
			if result != tt.expected {
				t.Errorf("normalizeModelName(%q) = %q; want %q", tt.input, result, tt.expected)
			}
		})
	}
}
