// Package main runs the first-wave live smoke for session.end -> worker -> review ready.
//
// Default mode co-locates HTTP app-server handlers and a worker loop in one process so
// the in-memory store can be exercised without Docker. When MYSQL_DSN is set, the same
// HTTP flow works against a shared MySQL-backed store (pair with a separate worker if desired).
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
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
	"github.com/FluentWork/fluentwork-backend/internal/aicost"
	"github.com/FluentWork/fluentwork-backend/internal/config"
	"github.com/FluentWork/fluentwork-backend/internal/httpserver"
	"github.com/FluentWork/fluentwork-backend/internal/reviewgen"
	"github.com/FluentWork/fluentwork-backend/internal/session"
	"github.com/FluentWork/fluentwork-backend/pkg/logx"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "smoke-review-ready FAILED: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	if strings.TrimSpace(os.Getenv("APP_ENV")) == "" {
		_ = os.Setenv("APP_ENV", "development")
	}
	cfg := config.Load()
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("config: %w", err)
	}

	logger := logx.New("smoke-review-ready")
	slog.SetDefault(logger)
	gin.SetMode(gin.ReleaseMode)

	accountStore, accountCloser, err := account.OpenStore(cfg, logger)
	if err != nil {
		return err
	}
	defer func() { _ = accountCloser() }()

	sessionStore, sessionCloser, err := session.OpenStore(cfg, logger)
	if err != nil {
		return err
	}
	defer func() { _ = sessionCloser() }()

	costStore, costCloser, err := aicost.OpenStore(cfg, logger)
	if err != nil {
		return err
	}
	defer func() { _ = costCloser() }()

	accountSvc := account.NewService(accountStore, session.Reassigner{Store: sessionStore}, cfg, logger)
	accountHandler := account.NewHandler(accountSvc)
	sessionSvc := session.NewService(sessionStore, cfg, logger)
	costSvc := aicost.NewService(costStore, logger)
	sessionSvc.SetCostRecorder(costSvc)
	reviewGenerator := reviewgen.ArkGenerator{
		BaseURL:  cfg.ArkBaseURL,
		APIKey:   cfg.ArkAPIKey,
		Endpoint: cfg.ArkReviewRefineEP,
		Logger:   logger.With("component", "reviewgen.ark"),
	}
	arkEnabled := reviewGenerator.Enabled()
	if arkEnabled {
		sessionSvc.SetReviewGenerator(reviewGenerator)
	}
	logger.Info("smoke review generator",
		"ark_review_enabled", arkEnabled,
		"ark_review_endpoint", cfg.ArkReviewRefineEP,
	)
	sessionHandler := session.NewHandler(sessionSvc, accountHandler)
	server := httpserver.New(cfg, logger, accountHandler, nil, sessionHandler, accountStore.Ping)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	baseURL := "http://" + listener.Addr().String()

	httpServer := &http.Server{
		Handler:           server.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	errCh := make(chan error, 2)
	go func() {
		errCh <- httpServer.Serve(listener)
	}()
	go func() {
		errCh <- runWorker(ctx, sessionSvc, "smoke-worker")
	}()

	defer func() {
		cancel()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()

	if err := waitHealthy(baseURL+"/healthz", 10*time.Second); err != nil {
		return err
	}

	evidence, err := exerciseReviewReady(baseURL, cfg.InternalAPIToken, arkEnabled)
	if err != nil {
		return err
	}

	if arkEnabled {
		if evidence.Generator != "ark-review-refine-v1" {
			return fmt.Errorf("expected generator ark-review-refine-v1 with Ark enabled, got %q", evidence.Generator)
		}
		logs, listErr := costSvc.ListRecent(context.Background(), "", 10)
		if listErr != nil {
			return fmt.Errorf("list ai cost logs: %w", listErr)
		}
		evidence.CostLogCount = len(logs)
		for _, row := range logs {
			if row.TaskType == "review.eval" {
				evidence.CostTaskType = row.TaskType
				evidence.CostModel = row.Model
				evidence.CostTokensIn = row.TokensIn
				evidence.CostTokensOut = row.TokensOut
				break
			}
		}
		if evidence.CostTaskType == "" {
			return fmt.Errorf("expected ai_cost_logs row task_type=review.eval, got %d rows", len(logs))
		}
		if evidence.ReadyWaitMS > 15000 {
			return fmt.Errorf("review ready wait %dms exceeds 15s SLA (duration_ms proxy)", evidence.ReadyWaitMS)
		}
	}

	encoded, err := json.MarshalIndent(evidence, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println("=== wave1 review-ready smoke PASS ===")
	fmt.Println(string(encoded))
	if cfg.MySQLDSN == "" {
		fmt.Println("mode: in-process memory store (HTTP handlers + worker loop co-located)")
	} else {
		fmt.Println("mode: MySQL-backed store (MYSQL_DSN set)")
	}
	if arkEnabled {
		fmt.Println("ark_path: enabled (asserted generator + ai_cost_logs + ready_wait_ms<=15000)")
	} else {
		fmt.Println("ark_path: disabled (stub-v1 fallback)")
	}
	return nil
}

func runWorker(ctx context.Context, svc *session.Service, workerID string) error {
	ticker := time.NewTicker(100 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return nil
		case <-ticker.C:
			_, err := svc.ProcessNextJob(ctx, workerID)
			if err != nil && !errors.Is(err, context.Canceled) {
				slog.Warn("smoke worker process", "err", err)
			}
		}
	}
}

func waitHealthy(url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		res, err := http.Get(url) //nolint:gosec // local smoke only
		if err == nil {
			_ = res.Body.Close()
			if res.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("healthz not ready at %s", url)
}

type smokeEvidence struct {
	SessionID      string          `json:"session_id"`
	ReviewStatus   string          `json:"review_status"`
	Generator      string          `json:"generator,omitempty"`
	UtteranceCount any             `json:"utterance_count,omitempty"`
	ReadyWaitMS    int64           `json:"ready_wait_ms,omitempty"`
	CostLogCount   int             `json:"cost_log_count,omitempty"`
	CostTaskType   string          `json:"cost_task_type,omitempty"`
	CostModel      string          `json:"cost_model,omitempty"`
	CostTokensIn   int             `json:"cost_tokens_in,omitempty"`
	CostTokensOut  int             `json:"cost_tokens_out,omitempty"`
	Review         json.RawMessage `json:"review,omitempty"`
	Steps          []string        `json:"steps"`
}

func exerciseReviewReady(baseURL, internalToken string, arkEnabled bool) (smokeEvidence, error) {
	steps := make([]string, 0, 8)
	client := &http.Client{Timeout: 10 * time.Second}

	guest, err := postJSON(client, baseURL+"/api/v1/auth/guest", "", map[string]any{
		"device_id": "smoke-review-ready-device",
	})
	if err != nil {
		return smokeEvidence{}, fmt.Errorf("guest auth: %w", err)
	}
	token, _ := guest["access_token"].(string)
	if token == "" {
		return smokeEvidence{}, fmt.Errorf("guest auth missing access_token: %#v", guest)
	}
	steps = append(steps, "guest auth")

	sceneType := "demo"
	if arkEnabled {
		sceneType = "standup"
	}
	created, err := postJSON(client, baseURL+"/api/v1/sessions", token, map[string]any{
		"scene_type": sceneType,
	})
	if err != nil {
		return smokeEvidence{}, fmt.Errorf("create session: %w", err)
	}
	sessionID, _ := created["session_id"].(string)
	if sessionID == "" {
		return smokeEvidence{}, fmt.Errorf("create session missing session_id: %#v", created)
	}
	steps = append(steps, "create session")

	if _, err := postJSON(client, baseURL+"/internal/v1/sessions/activate", "", map[string]any{
		"session_id": sessionID,
	}, header{"X-Internal-Token", internalToken}); err != nil {
		return smokeEvidence{}, fmt.Errorf("activate session: %w", err)
	}
	steps = append(steps, "activate session")

	pending, err := getJSON(client, baseURL+"/api/v1/sessions/"+sessionID+"/review", token)
	if err != nil {
		return smokeEvidence{}, fmt.Errorf("review pending: %w", err)
	}
	if status, _ := pending["status"].(string); status != session.ReviewPollPending {
		return smokeEvidence{}, fmt.Errorf("expected pending review before end, got %#v", pending)
	}
	steps = append(steps, "review pending before end")

	utterances := []map[string]any{
		{"seq": 1, "speaker": "ai", "text": "ready"},
		{"seq": 2, "speaker": "user", "text": "hello fluentwork smoke"},
	}
	if arkEnabled {
		// Richer English transcript so B15 quote/schema checks can pass on live Ark.
		utterances = []map[string]any{
			{"seq": 1, "speaker": "ai", "text": "What is blocking you today?"},
			{"seq": 2, "speaker": "user", "text": "I will sync up with the team tomorrow. I am blocked on the API review."},
		}
	}

	if _, err := postJSON(client, baseURL+"/internal/v1/sessions/end", "", map[string]any{
		"session_id":   sessionID,
		"duration_sec": 18,
		"reason":       "smoke",
		"utterances":   utterances,
	}, header{"X-Internal-Token", internalToken}); err != nil {
		return smokeEvidence{}, fmt.Errorf("session end: %w", err)
	}
	steps = append(steps, "session.end persisted + job enqueued")

	pollBudget := 15 * time.Second
	if arkEnabled {
		pollBudget = 45 * time.Second
	}
	waitStarted := time.Now()
	var ready map[string]any
	deadline := waitStarted.Add(pollBudget)
	for time.Now().Before(deadline) {
		ready, err = getJSON(client, baseURL+"/api/v1/sessions/"+sessionID+"/review", token)
		if err != nil {
			return smokeEvidence{}, fmt.Errorf("poll review: %w", err)
		}
		status, _ := ready["status"].(string)
		switch status {
		case session.ReviewPollReady:
			steps = append(steps, "review ready")
			return buildEvidence(sessionID, ready, steps, time.Since(waitStarted).Milliseconds())
		case session.ReviewPollFailed:
			return smokeEvidence{}, fmt.Errorf("review failed: %#v", ready)
		}
		time.Sleep(100 * time.Millisecond)
	}
	return smokeEvidence{}, fmt.Errorf("timed out waiting for review ready; last=%#v", ready)
}

func buildEvidence(sessionID string, ready map[string]any, steps []string, readyWaitMS int64) (smokeEvidence, error) {
	raw, err := json.Marshal(ready["review"])
	if err != nil {
		return smokeEvidence{}, err
	}
	var reviewDoc map[string]any
	_ = json.Unmarshal(raw, &reviewDoc)
	generator, _ := reviewDoc["generator"].(string)
	if generator == "" {
		return smokeEvidence{}, fmt.Errorf("ready review missing generator: %#v", ready)
	}
	return smokeEvidence{
		SessionID:      sessionID,
		ReviewStatus:   session.ReviewPollReady,
		Generator:      generator,
		UtteranceCount: reviewDoc["utterance_count"],
		ReadyWaitMS:    readyWaitMS,
		Review:         raw,
		Steps:          steps,
	}, nil
}

type header struct {
	key   string
	value string
}

func postJSON(client *http.Client, url, bearer string, body any, headers ...header) (map[string]any, error) {
	payload, err := json.Marshal(body)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(payload))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	for _, h := range headers {
		req.Header.Set(h.key, h.value)
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	return decodeMap(res)
}

func getJSON(client *http.Client, url, bearer string) (map[string]any, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if bearer != "" {
		req.Header.Set("Authorization", "Bearer "+bearer)
	}
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	return decodeMap(res)
}

func decodeMap(res *http.Response) (map[string]any, error) {
	body, err := io.ReadAll(res.Body)
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("status=%d body=%s", res.StatusCode, strings.TrimSpace(string(body)))
	}
	var out map[string]any
	if err := json.Unmarshal(body, &out); err != nil {
		return nil, fmt.Errorf("decode json: %w body=%s", err, strings.TrimSpace(string(body)))
	}
	return out, nil
}
