// Package main starts the FluentWork app-server HTTP process.
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"net/http"
	"os"
	"os/signal"
	"strings"
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/FluentWork/fluentwork-backend/internal/account"
	"github.com/FluentWork/fluentwork-backend/internal/aicost"
	"github.com/FluentWork/fluentwork-backend/internal/config"
	"github.com/FluentWork/fluentwork-backend/internal/content"
	"github.com/FluentWork/fluentwork-backend/internal/content/tts"
	"github.com/FluentWork/fluentwork-backend/internal/corpus"
	"github.com/FluentWork/fluentwork-backend/internal/drill"
	"github.com/FluentWork/fluentwork-backend/internal/httpserver"
	reviewpkg "github.com/FluentWork/fluentwork-backend/internal/review"
	"github.com/FluentWork/fluentwork-backend/internal/reviewgen"
	"github.com/FluentWork/fluentwork-backend/internal/session"
	"github.com/FluentWork/fluentwork-backend/internal/voicepoc"
	"github.com/FluentWork/fluentwork-backend/pkg/buildinfo"
	"github.com/FluentWork/fluentwork-backend/pkg/logx"
)

func main() {
	if err := run(); err != nil {
		slog.Error("app-server exited", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg := config.Load()
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("config: %w", err)
	}

	logger := logx.New("app-server")
	slog.SetDefault(logger)
	gin.SetMode(gin.ReleaseMode)

	accountStore, accountCloser, err := account.OpenStore(cfg, logger)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := accountCloser(); closeErr != nil {
			logger.Error("closing account store", "err", closeErr)
		}
	}()

	sessionStore, sessionCloser, err := session.OpenStore(cfg, logger)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := sessionCloser(); closeErr != nil {
			logger.Error("closing session store", "err", closeErr)
		}
	}()

	corpusStore, corpusCloser, err := corpus.OpenStore(cfg, logger)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := corpusCloser(); closeErr != nil {
			logger.Error("closing corpus store", "err", closeErr)
		}
	}()

	contentStore, contentCloser, err := content.OpenStore(cfg, logger)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := contentCloser(); closeErr != nil {
			logger.Error("closing content store", "err", closeErr)
		}
	}()

	// B16 — ai_cost_logs query endpoint. The cost ledger is written atomically
	// by the session store (#21 followup); here we only need a read-side
	// service so /internal/v1/ai-cost-logs can serve ops and smoke harnesses.
	costStore, costCloser, err := aicost.OpenStore(cfg, logger)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := costCloser(); closeErr != nil {
			logger.Error("closing aicost store", "err", closeErr)
		}
	}()
	costSvc := aicost.NewService(costStore, logger)
	costHandler := aicost.NewHandler(costSvc)

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
	sessionSvc := session.NewService(sessionStore, cfg, logger)
	reviewGenerator := reviewgen.ArkGenerator{
		BaseURL:  cfg.ArkBaseURL,
		APIKey:   cfg.ArkAPIKey,
		Endpoint: cfg.ArkReviewRefineEP,
		Logger:   logger.With("component", "reviewgen.ark"),
	}
	if reviewGenerator.Enabled() {
		sessionSvc.SetReviewGenerator(reviewGenerator)
	}
	sessionHandler := session.NewHandler(sessionSvc, accountHandler)
	reviewEval := reviewpkg.NewService(sessionStore, corpusStore, drill.NewArkCompleter(cfg), logger)
	sessionSvc.SetEvalProcessor(reviewEval)
	ttsHandler := tts.NewHandler(newTTSProvider(logger))

	drillRecords, drillCloser, err := drill.OpenRecordStore(cfg, logger)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := drillCloser(); closeErr != nil {
			logger.Error("closing drill record store", "err", closeErr)
		}
	}()
	drillSvc := drill.NewService(corpusStore, drillRecords, &drill.LLMJudge{LLM: drill.NewArkCompleter(cfg)}, logger)
	drillHandler := drill.NewHandler(drillSvc, accountHandler)
	privacy := account.NewPrivacyService(accountStore, []account.DataWiper{
		corpus.PrivacyWiper{Store: corpusStore},
		session.PrivacyWiper{Store: sessionStore},
		aicost.PrivacyWiper{Store: costStore},
	}, []account.HardDeleter{
		drill.RecordWiper{Store: drillRecords},
	}, logger)
	accountHandler.SetPrivacy(privacy)

	server := httpserver.New(cfg, logger, accountHandler, corpusHandler, contentHandler, sessionHandler, costHandler, ttsHandler, drillHandler, accountStore.Ping)

	httpServer := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           server.Handler(),
		ReadHeaderTimeout: 5 * time.Second,
		ReadTimeout:       15 * time.Second,
		WriteTimeout:      15 * time.Second,
		IdleTimeout:       60 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	// Local no-Docker mode uses in-memory stores, which cannot be shared with a
	// separate `cmd/worker` process. Run the review consumer in-process so
	// session.end -> review ready works out of the box. MySQL deployments keep
	// using the standalone worker unless explicitly enabled.
	runWorker := strings.TrimSpace(os.Getenv("APP_RUN_REVIEW_WORKER"))
	if runWorker == "" {
		if cfg.MySQLDSN == "" {
			runWorker = "1"
		} else {
			runWorker = "0"
		}
	}
	if runWorker == "1" {
		workerID := envOr("WORKER_ID", "app-server-inproc")
		pollEvery := durationOr("WORKER_POLL_INTERVAL", 500*time.Millisecond)
		reviewEnabled := reviewGenerator.Enabled()
		logger.Info("in-process review worker enabled",
			"worker_id", workerID,
			"poll_interval", pollEvery.String(),
			"ark_review_enabled", reviewEnabled,
			"ark_review_endpoint", cfg.ArkReviewRefineEP,
		)
		go func() {
			for {
				select {
				case <-ctx.Done():
					return
				default:
				}
				ok, err := sessionSvc.ProcessNextJob(ctx, workerID)
				if err != nil && !errors.Is(err, context.Canceled) {
					logger.Warn("in-process review worker: process job", "err", err)
				}
				if ok {
					continue
				}
				timer := time.NewTimer(pollEvery)
				select {
				case <-ctx.Done():
					timer.Stop()
					return
				case <-timer.C:
				}
			}
		}()
	}

	errCh := make(chan error, 1)
	go func() {
		logger.Info("app-server listening",
			"addr", cfg.HTTPAddr,
			"service", "app-server",
			"repository", buildinfo.Repository,
		)
		errCh <- httpServer.ListenAndServe()
	}()

	select {
	case <-ctx.Done():
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
		defer cancel()
		if err := httpServer.Shutdown(shutdownCtx); err != nil {
			_ = httpServer.Close()
			return fmt.Errorf("graceful shutdown: %w", err)
		}
		return nil
	case err := <-errCh:
		if errors.Is(err, http.ErrServerClosed) {
			return nil
		}
		return err
	}
}

func envOr(key, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}

func newTTSProvider(logger *slog.Logger) tts.Provider {
	apiKey := firstNonEmpty(os.Getenv("VOLC_SPEECH_API_KEY"), os.Getenv("VOLC_SPEECH_API_KEY_DEV"))
	if apiKey == "" {
		if logger != nil {
			logger.Info("tts provider disabled: VOLC_SPEECH_API_KEY is empty")
		}
		return nil
	}
	if logger == nil {
		logger = slog.Default()
	}
	primary := &tts.VolcStreamingProvider{
		APIKey:     apiKey,
		ResourceID: envOr("VOLC_SPEECH_RESOURCE_TTS", "seed-tts-2.0"),
		Logger:     logger.With("component", "tts.volc_streaming"),
	}
	fallback := &tts.VolcDuplexFallbackProvider{
		Config: voicepoc.DuplexConfig{
			APIKey: apiKey,
			Logger: logger.With("component", "tts.volc_duplex"),
		},
		Logger: logger.With("component", "tts.volc_duplex_fallback"),
	}
	return tts.NewManager(primary, fallback)
}

func durationOr(key string, fallback time.Duration) time.Duration {
	raw := os.Getenv(key)
	if raw == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return fallback
	}
	return parsed
}
