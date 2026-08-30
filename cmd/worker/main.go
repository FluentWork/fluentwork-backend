// Package main starts the FluentWork async worker (B5 review pipeline).
package main

import (
	"context"
	"errors"
	"fmt"
	"log/slog"
	"os"
	"os/signal"
	"syscall"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/aicost"
	"github.com/FluentWork/fluentwork-backend/internal/config"
	"github.com/FluentWork/fluentwork-backend/internal/session"
	"github.com/FluentWork/fluentwork-backend/pkg/buildinfo"
	"github.com/FluentWork/fluentwork-backend/pkg/logx"
)

func main() {
	if err := run(); err != nil {
		slog.Error("worker exited", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg := config.Load()
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("config: %w", err)
	}

	logger := logx.New("worker")
	slog.SetDefault(logger)

	store, closer, err := session.OpenStore(cfg, logger)
	if err != nil {
		return err
	}
	defer func() {
		if closeErr := closer(); closeErr != nil {
			logger.Error("closing session store", "err", closeErr)
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

	svc := session.NewService(store, cfg, logger)
	svc.SetCostRecorder(aicost.NewService(costStore, logger))
	workerID := envOr("WORKER_ID", "worker-1")
	pollEvery := durationOr("WORKER_POLL_INTERVAL", 500*time.Millisecond)

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	logger.Info("worker listening",
		"service", "worker",
		"worker_id", workerID,
		"poll_interval", pollEvery.String(),
		"repository", buildinfo.Repository,
	)

	for {
		select {
		case <-ctx.Done():
			return nil
		default:
		}

		ok, err := svc.ProcessNextJob(ctx, workerID)
		if err != nil && !errors.Is(err, context.Canceled) {
			logger.Warn("process job", "err", err)
		}
		if ok {
			continue
		}

		timer := time.NewTimer(pollEvery)
		select {
		case <-ctx.Done():
			timer.Stop()
			return nil
		case <-timer.C:
		}
	}
}

func envOr(key, fallback string) string {
	value := os.Getenv(key)
	if value == "" {
		return fallback
	}
	return value
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
