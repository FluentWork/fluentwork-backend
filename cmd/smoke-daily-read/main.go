// Package main runs a live smoke for B11 daily reads.
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
	"github.com/FluentWork/fluentwork-backend/internal/content"
	"github.com/FluentWork/fluentwork-backend/internal/corpus"
	"github.com/FluentWork/fluentwork-backend/internal/httpserver"
	"github.com/FluentWork/fluentwork-backend/internal/session"
	"github.com/FluentWork/fluentwork-backend/pkg/logx"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "smoke-daily-read FAILED: %v\n", err)
		os.Exit(1)
	}
}

type smokeEvidence struct {
	GenDate     string   `json:"gen_date"`
	Status      string   `json:"status"`
	Generator   string   `json:"generator"`
	DailyReadID string   `json:"daily_read_id"`
	Steps       []string `json:"steps"`
}

func run() error {
	if strings.TrimSpace(os.Getenv("APP_ENV")) == "" {
		_ = os.Setenv("APP_ENV", "development")
	}
	cfg := config.Load()
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("config: %w", err)
	}

	logger := logx.New("smoke-daily-read")
	slog.SetDefault(logger)
	gin.SetMode(gin.ReleaseMode)

	accountStore := account.NewMemoryStore()
	sessionStore := session.NewMemoryStore()
	corpusStore := corpus.NewMemoryStore()
	contentStore := content.NewMemoryStore()

	accountSvc := account.NewService(accountStore, account.ChainReassigner{
		session.Reassigner{Store: sessionStore},
		corpus.Reassigner{Store: corpusStore},
		content.Reassigner{Store: contentStore},
	}, cfg, logger)
	accountHandler := account.NewHandler(accountSvc)
	corpusSvc := corpus.NewService(corpusStore, logger)
	corpusHandler := corpus.NewHandler(corpusSvc, accountHandler)
	contentSvc := content.NewService(contentStore, content.CorpusBlockSource{Store: corpusStore}, logger)
	contentHandler := content.NewHandler(contentSvc, accountHandler)
	server := httpserver.New(cfg, logger, accountHandler, corpusHandler, contentHandler, nil, accountStore.Ping)

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
	steps := make([]string, 0, 6)

	guest, err := postJSON(client, baseURL+"/api/v1/auth/guest", "", map[string]any{
		"device_id": "smoke-daily-read-device",
	})
	if err != nil {
		return fmt.Errorf("guest auth: %w", err)
	}
	token, _ := guest["access_token"].(string)
	if token == "" {
		return fmt.Errorf("guest auth missing access_token: %#v", guest)
	}
	steps = append(steps, "guest auth")

	today, err := getJSON(client, baseURL+"/api/v1/daily-reads/today", token)
	if err != nil {
		return fmt.Errorf("daily read today: %w", err)
	}
	status, _ := today["status"].(string)
	genDate, _ := today["gen_date"].(string)
	readDoc, _ := today["daily_read"].(map[string]any)
	if status != content.StatusReady || readDoc == nil {
		return fmt.Errorf("unexpected today response: %#v", today)
	}
	readID, _ := readDoc["id"].(string)
	generator, _ := readDoc["generator"].(string)
	if readID == "" || generator == "" {
		return fmt.Errorf("today ready payload incomplete: %#v", today)
	}
	steps = append(steps, "daily read today")

	follow, err := postJSON(client, baseURL+"/api/v1/daily-reads/"+readID+"/follow-read", token, map[string]any{})
	if err != nil {
		return fmt.Errorf("follow read: %w", err)
	}
	recorded, _ := follow["recorded"].(bool)
	if !recorded {
		return fmt.Errorf("unexpected follow response: %#v", follow)
	}
	steps = append(steps, "follow read")

	encoded, err := json.MarshalIndent(smokeEvidence{
		GenDate:     genDate,
		Status:      status,
		Generator:   generator,
		DailyReadID: readID,
		Steps:       steps,
	}, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println("=== B11 daily-read smoke PASS ===")
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
