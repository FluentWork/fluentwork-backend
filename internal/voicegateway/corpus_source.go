package voicegateway

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"net/url"
	"strings"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/session"
)

// HTTPCorpusSource implements session.BlockSource by reading phrase blocks
// from the app-server internal corpus endpoint. The gateway already talks to
// app-server for tickets and session lifecycle; keeping the corpus in the same
// process means B12 hit detection uses the app-server's source of truth in
// both memory-dev and MySQL deployments.
type HTTPCorpusSource struct {
	BaseURL    string
	Token      string
	HTTPClient *http.Client
	Logger     *slog.Logger
}

// NewHTTPCorpusSource builds the app-server corpus client.
func NewHTTPCorpusSource(baseURL, token string, logger *slog.Logger) *HTTPCorpusSource {
	if logger == nil {
		logger = slog.Default()
	}
	return &HTTPCorpusSource{
		BaseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		Token:   strings.TrimSpace(token),
		Logger:  logger.With("component", "voicegateway.corpus_source"),
	}
}

type corpusCandidatesResponse struct {
	Items []struct {
		ID             string `json:"id"`
		IntentZH       string `json:"intent_zh"`
		ExpressionEN   string `json:"expression_en"`
		AnchorUserSaid string `json:"anchor_user_said"`
		SceneTag       string `json:"scene_tag"`
		FunctionTag    string `json:"function_tag"`
	} `json:"items"`
}

// CandidatesForUser fetches up to 50 phrase blocks owned by userID.
func (s *HTTPCorpusSource) CandidatesForUser(ctx context.Context, userID string) ([]session.BlockCandidate, error) {
	if s == nil || strings.TrimSpace(s.BaseURL) == "" {
		return nil, fmt.Errorf("corpus source: app-server base URL is required")
	}
	reqURL := s.BaseURL + "/internal/v1/corpus/blocks?user_id=" + url.QueryEscape(strings.TrimSpace(userID))
	req, err := http.NewRequestWithContext(ctx, http.MethodGet, reqURL, nil)
	if err != nil {
		return nil, err
	}
	req.Header.Set("X-Internal-Token", s.Token)

	client := s.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 700 * time.Millisecond}
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, fmt.Errorf("fetch corpus candidates: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if resp.StatusCode != http.StatusOK {
		return nil, fmt.Errorf("fetch corpus candidates: status %d: %s", resp.StatusCode, strings.TrimSpace(string(body)))
	}
	var out corpusCandidatesResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode corpus candidates: %w", err)
	}

	candidates := make([]session.BlockCandidate, 0, len(out.Items))
	for _, item := range out.Items {
		candidates = append(candidates, session.BlockCandidate{
			ID:             item.ID,
			IntentZH:       item.IntentZH,
			ExpressionEN:   item.ExpressionEN,
			AnchorUserSaid: item.AnchorUserSaid,
			SceneTag:       item.SceneTag,
			FunctionTag:    item.FunctionTag,
		})
	}
	return candidates, nil
}
