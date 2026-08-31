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
	"syscall"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/FluentWork/fluentwork-backend/internal/account"
	"github.com/FluentWork/fluentwork-backend/internal/aicost"
	"github.com/FluentWork/fluentwork-backend/internal/config"
	"github.com/FluentWork/fluentwork-backend/internal/content"
	"github.com/FluentWork/fluentwork-backend/internal/corpus"
	"github.com/FluentWork/fluentwork-backend/internal/httpserver"
	"github.com/FluentWork/fluentwork-backend/internal/reviewgen"
	"github.com/FluentWork/fluentwork-backend/internal/session"
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

	costStore, costCloser, err := aicost.OpenStore(cfg, logger)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := costCloser(); closeErr != nil {
			logger.Error("closing ai cost store", "err", closeErr)
		}
	}()

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
	sessionSvc.SetCostRecorder(aicost.NewService(costStore, logger))
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
	server := httpserver.New(cfg, logger, accountHandler, corpusHandler, contentHandler, sessionHandler, accountStore.Ping)

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
