// Package main runs a live smoke for B10 corpus batch-accept/list/favorite/delete.
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/FluentWork/fluentwork-backend/internal/account"
	"github.com/FluentWork/fluentwork-backend/internal/config"
	"github.com/FluentWork/fluentwork-backend/internal/corpus"
	"github.com/FluentWork/fluentwork-backend/internal/httpserver"
	"github.com/FluentWork/fluentwork-backend/internal/session"
	"github.com/FluentWork/fluentwork-backend/pkg/logx"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "smoke-corpus FAILED: %v\n", err)
		os.Exit(1)
	}
}

type smokeEvidence struct {
	AcceptedCount int      `json:"accepted_count"`
	ListedCount   int      `json:"listed_count"`
	KeywordHits   int      `json:"keyword_hits"`
	BlockID       string   `json:"block_id"`
	Steps         []string `json:"steps"`
}

func run() error {
	if strings.TrimSpace(os.Getenv("APP_ENV")) == "" {
		_ = os.Setenv("APP_ENV", "development")
	}
	cfg := config.Load()
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("config: %w", err)
	}

	logger := logx.New("smoke-corpus")
	slog.SetDefault(logger)
	gin.SetMode(gin.ReleaseMode)

	accountStore := account.NewMemoryStore()
	sessionStore := session.NewMemoryStore()
	corpusStore := corpus.NewMemoryStore()

	accountSvc := account.NewService(accountStore, account.ChainReassigner{
		session.Reassigner{Store: sessionStore},
		corpus.Reassigner{Store: corpusStore},
	}, cfg, logger)
	accountHandler := account.NewHandler(accountSvc)
	corpusSvc := corpus.NewService(corpusStore, logger)
	corpusHandler := corpus.NewHandler(corpusSvc, accountHandler)
	server := httpserver.New(cfg, logger, accountHandler, corpusHandler, nil, accountStore.Ping)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	baseURL := "http://" + listener.Addr().String()
	httpServer := &http.Server{
		Handler:           server.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}
	go func() { _ = httpServer.Serve(listener) }()
	defer func() {
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()

	client := &http.Client{Timeout: 10 * time.Second}
	steps := make([]string, 0, 8)

	guest, err := postJSON(client, baseURL+"/api/v1/auth/guest", "", map[string]any{
		"device_id": "smoke-corpus-device",
	})
	if err != nil {
		return fmt.Errorf("guest auth: %w", err)
	}
	token, _ := guest["access_token"].(string)
	if token == "" {
		return fmt.Errorf("guest auth missing access_token: %#v", guest)
	}
	steps = append(steps, "guest auth")

	accepted, err := postJSON(client, baseURL+"/api/v1/corpus/blocks/batch-accept", token, map[string]any{
		"source_session_id": "smoke-session-1",
		"blocks": []map[string]any{{
			"intent_zh":        "说明阻塞",
			"expression_en":    "I'm blocked on the API review.",
			"anchor_user_said": "I am blocked on the API review.",
			"scene_tag":        "standup",
			"function_tag":     "report",
		}},
	})
	if err != nil {
		return fmt.Errorf("batch accept: %w", err)
	}
	acceptedCount, _ := accepted["accepted_count"].(float64)
	items, _ := accepted["items"].([]any)
	if int(acceptedCount) != 1 || len(items) != 1 {
		return fmt.Errorf("unexpected batch accept: %#v", accepted)
	}
	firstItem, _ := items[0].(map[string]any)
	blockID, _ := firstItem["id"].(string)
	if blockID == "" {
		return fmt.Errorf("batch accept missing block id: %#v", accepted)
	}
	steps = append(steps, "batch accept")

	listed, err := getJSON(client, baseURL+"/api/v1/corpus/blocks?scene=standup", token)
	if err != nil {
		return fmt.Errorf("list blocks: %w", err)
	}
	listItems, _ := listed["items"].([]any)
	if len(listItems) != 1 {
		return fmt.Errorf("unexpected list response: %#v", listed)
	}
	steps = append(steps, "list blocks")

	keyword, err := getJSON(client, baseURL+"/api/v1/corpus/blocks?kw=blocked+on+the+API+review", token)
	if err != nil {
		return fmt.Errorf("keyword list: %w", err)
	}
	keywordItems, _ := keyword["items"].([]any)
	if len(keywordItems) != 1 {
		return fmt.Errorf("expected keyword match on anchor_user_said, got %#v", keyword)
	}
	steps = append(steps, "keyword search")

	if _, err := postJSON(client, baseURL+"/api/v1/corpus/blocks/"+blockID+"/favorite", token, map[string]any{
		"is_favorite": true,
		"pinned":      true,
	}); err != nil {
		return fmt.Errorf("favorite: %w", err)
	}
	steps = append(steps, "favorite")

	if _, err := requestJSON(client, http.MethodDelete, baseURL+"/api/v1/corpus/blocks/"+blockID, token, nil); err != nil {
		return fmt.Errorf("delete: %w", err)
	}
	steps = append(steps, "soft delete")

	evidence := smokeEvidence{
		AcceptedCount: int(acceptedCount),
		ListedCount:   len(listItems),
		KeywordHits:   len(keywordItems),
		BlockID:       blockID,
		Steps:         steps,
	}
	encoded, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println("=== B10 corpus smoke PASS ===")
	fmt.Println(string(encoded))
	return nil
}

func postJSON(client *http.Client, url, bearer string, body any) (map[string]any, error) {
	return requestJSON(client, http.MethodPost, url, bearer, body)
}

func getJSON(client *http.Client, url, bearer string) (map[string]any, error) {
	return requestJSON(client, http.MethodGet, url, bearer, nil)
}

func requestJSON(client *http.Client, method, url, bearer string, body any) (map[string]any, error) {
	var reader io.Reader
	if body != nil {
		payload, err := json.Marshal(body)
		if err != nil {
			return nil, err
		}
		reader = bytes.NewReader(payload)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		return nil, err
	}
	if body != nil {
		req.Header.Set("Content-Type", "application/json")
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	resp, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = resp.Body.Close() }()
	raw, err := io.ReadAll(resp.Body)
	if err != nil {
		return nil, err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		return nil, fmt.Errorf("%s %s -> %d body=%s", method, url, resp.StatusCode, string(raw))
	}
	var out map[string]any
	if len(raw) == 0 {
		return out, nil
	}
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, err
	}
	return out, nil
}
