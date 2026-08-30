// Package main starts the FluentWork voice-gateway WSS process (B3/B4).
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

	"github.com/FluentWork/fluentwork-backend/internal/voicegateway"
	"github.com/FluentWork/fluentwork-backend/pkg/buildinfo"
	"github.com/FluentWork/fluentwork-backend/pkg/logx"
)

func main() {
	if err := run(); err != nil {
		slog.Error("voice-gateway exited", "err", err)
		os.Exit(1)
	}
}

func run() error {
	cfg := voicegateway.LoadConfig()
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("config: %w", err)
	}

	logger := logx.New("voice-gateway")
	slog.SetDefault(logger)

	consumer := &voicegateway.HTTPTicketConsumer{
		BaseURL: cfg.AppServerInternalURL,
		Token:   cfg.InternalAPIToken,
		Logger:  logger,
	}
	lifecycle := &voicegateway.HTTPSessionClient{
		BaseURL: cfg.AppServerInternalURL,
		Token:   cfg.InternalAPIToken,
		Logger:  logger,
	}
	handler := voicegateway.NewHandler(consumer, lifecycle, logger, voicegateway.Options{
		InsecureSkipOrigin: cfg.IsDevelopment(),
		IdleTimeout:        cfg.IdleTimeout,
	})
	mux := http.NewServeMux()
	handler.Mount(mux)

	httpServer := &http.Server{
		Addr:              cfg.HTTPAddr,
		Handler:           mux,
		ReadHeaderTimeout: 5 * time.Second,
	}

	ctx, stop := signal.NotifyContext(context.Background(), syscall.SIGINT, syscall.SIGTERM)
	defer stop()

	errCh := make(chan error, 1)
	go func() {
		logger.Info("voice-gateway listening",
			"addr", cfg.HTTPAddr,
			"app_server", cfg.AppServerInternalURL,
			"service", "voice-gateway",
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
