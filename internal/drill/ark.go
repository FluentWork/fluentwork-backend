package drill

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/config"
)

// ArkCompleter calls Ark chat-completions. Used as the B16 seam until AIOrchestrator lands.
type ArkCompleter struct {
	BaseURL    string
	APIKey     string
	Model      string
	HTTPClient *http.Client
}

// NewArkCompleter returns an Ark Completer when API key + model are configured.
func NewArkCompleter(cfg config.Config) Completer {
	model := strings.TrimSpace(os.Getenv("ARK_EP_DRILL"))
	if model == "" {
		model = strings.TrimSpace(cfg.ArkReviewRefineEP)
	}
	if strings.TrimSpace(cfg.ArkAPIKey) == "" || model == "" {
		return nil
	}
	return &ArkCompleter{
		BaseURL: cfg.ArkBaseURL,
		APIKey:  cfg.ArkAPIKey,
		Model:   model,
	}
}

type arkChatRequest struct {
	Model          string           `json:"model"`
	Messages       []arkChatMessage `json:"messages"`
	MaxTokens      int              `json:"max_tokens"`
	Temperature    float64          `json:"temperature"`
	ResponseFormat map[string]any   `json:"response_format,omitempty"`
	Thinking       map[string]any   `json:"thinking,omitempty"`
}

type arkChatMessage struct {
	Role    string `json:"role"`
	Content string `json:"content"`
}

type arkChatResponse struct {
	Choices []struct {
		Message arkChatMessage `json:"message"`
	} `json:"choices"`
}

// Complete implements Completer.
func (a *ArkCompleter) Complete(ctx context.Context, prompt string) (string, error) {
	if a == nil || strings.TrimSpace(a.APIKey) == "" || strings.TrimSpace(a.Model) == "" {
		return "", fmt.Errorf("ark drill completer is not configured")
	}
	payload, err := json.Marshal(arkChatRequest{
		Model: a.Model,
		Messages: []arkChatMessage{
			{Role: "user", Content: prompt},
		},
		MaxTokens:   200,
		Temperature: 0,
		ResponseFormat: map[string]any{
			"type": "json_object",
		},
		Thinking: map[string]any{"type": "disabled"},
	})
	if err != nil {
		return "", err
	}
	client := a.HTTPClient
	if client == nil {
		client = &http.Client{
			Timeout: JudgeTimeout + 200*time.Millisecond,
			Transport: &http.Transport{
				Proxy:               http.ProxyFromEnvironment,
				ForceAttemptHTTP2:   false,
				DialContext:         (&net.Dialer{Timeout: time.Second, KeepAlive: 30 * time.Second}).DialContext,
				TLSHandshakeTimeout: time.Second,
			},
		}
	}
	base := strings.TrimRight(a.BaseURL, "/")
	if base == "" {
		base = "https://ark.cn-beijing.volces.com/api/v3"
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/chat/completions", bytes.NewReader(payload))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+strings.TrimSpace(a.APIKey))
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return "", err
	}
	if resp.StatusCode != http.StatusOK {
		return "", fmt.Errorf("ark chat completions http=%d body=%s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var decoded arkChatResponse
	if err := json.Unmarshal(body, &decoded); err != nil {
		return "", err
	}
	if len(decoded.Choices) == 0 {
		return "", fmt.Errorf("ark response missing choices")
	}
	return decoded.Choices[0].Message.Content, nil
}
