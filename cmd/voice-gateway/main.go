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
	"strings"
	"syscall"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/session"
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
	provider := voicegateway.NewVoiceProvider(cfg, logger)
	handler := voicegateway.NewHandler(
		consumer,
		lifecycle,
		provider,
		logger,
		voicegateway.Options{
			InsecureSkipOrigin: cfg.IsDevelopment(),
			IdleTimeout:        cfg.IdleTimeout,
		},
	)
	// Dev-only B12 wiring: when the dev-echo provider is active, drive the
	// same BadgeEmitter path the B12 handler integration tests exercise.
	// The block source is derived from the echo phrase itself so the loop is
	// self-contained (no DB, no app-server corpus dependency). Production
	// providers do NOT get an emitter here until a corpus-backed source is
	// wired (separate B12 production wiring item).
	if echoProvider, ok := provider.(voicegateway.DevEchoVoiceProvider); ok {
		echoText := strings.TrimSpace(echoProvider.EchoText)
		if echoText == "" {
			logger.Warn("dev-echo provider has empty VOICE_DEV_ECHO_TEXT; feedback.badge emitter disabled")
		} else {
			detector := session.NewHitDetector(echoBlockSource{echoText: echoText})
			handler.SetBadgeEmitter(
				voicegateway.NewBadgeEmitter(detector, logger, voicegateway.BadgeEmitterOptions{}),
			)
			logger.Info("dev-echo feedback.badge emitter wired",
				"echo_text", echoText,
				"phrase_block_id", "block-dev-echo",
			)
		}
	}
	// B13: enable client ASR gate when VOICE_CLIENT_ASR_REQUIRED is set.
	if os.Getenv("VOICE_CLIENT_ASR_REQUIRED") == "1" || os.Getenv("VOICE_CLIENT_ASR_REQUIRED") == "true" {
		handler.SetClientASRRequired(true)
		logger.Info("B13 client ASR gate enabled", "VOICE_CLIENT_ASR_REQUIRED", "true")
	}
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

// echoBlockSource is a dev-only session.BlockSource whose single candidate is
// the exact text DevEchoVoiceProvider echoes back as ServerASRText. Any
// non-empty echo text therefore always scores a 1.0 hit, which is what makes
// the local "speak → feedback.badge" acceptance loop deterministic.
type echoBlockSource struct {
	echoText string
}

func (s echoBlockSource) CandidatesForUser(_ context.Context, _ string) ([]session.BlockCandidate, error) {
	return []session.BlockCandidate{
		{
			ID:             "block-dev-echo",
			ExpressionEN:   s.echoText,
			IntentZH:       "开发验证",
			AnchorUserSaid: s.echoText,
		},
	}, nil
}
