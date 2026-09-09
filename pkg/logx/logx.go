// Package logx provides structured logging helpers for FluentWork services.
package logx

import (
	"log/slog"
	"os"
	"time"

	"github.com/FluentWork/fluentwork-backend/pkg/buildinfo"
)

// New creates the default JSON logger for backend processes.
func New(service string) *slog.Logger {
	logger := slog.New(slog.NewJSONHandler(os.Stdout, &slog.HandlerOptions{
		Level: slog.LevelInfo,
	}))
	if service != "" {
		logger = logger.With(
			"service", service,
			"repository", buildinfo.Repository,
		)
	}
	return logger
}

// With adds structured attributes onto a logger, defaulting to slog.Default().
func With(logger *slog.Logger, attrs ...any) *slog.Logger {
	if logger == nil {
		logger = slog.Default()
	}
	if len(attrs) == 0 {
		return logger
	}
	return logger.With(attrs...)
}

// Segment measures one tracked operation and writes consistent start/end logs.
type Segment struct {
	logger *slog.Logger
	event  string
	start  time.Time
	attrs  []any
}

// Begin starts a named operation segment.
func Begin(logger *slog.Logger, event string, attrs ...any) *Segment {
	logger = With(logger)
	seg := &Segment{
		logger: logger,
		event:  event,
		start:  time.Now(),
		attrs:  append([]any(nil), attrs...),
	}
	logger.Info(event+".start", attrs...)
	return seg
}

// End writes the segment completion log.
//
// Fields always include duration_ms and ts (unix millis). outcome defaults to
// ok/error from err, but an explicit "outcome" attr (ok|partial|timeout|error)
// wins so collectTurn can log the real TurnOutcome instead of a nil-error "ok".
func (s *Segment) End(err error, attrs ...any) {
	if s == nil || s.logger == nil {
		return
	}
	outcome := "ok"
	if err != nil {
		outcome = "error"
	}
	fields := append([]any{}, s.attrs...)
	fields = append(fields, attrs...)
	fields = append(fields,
		"duration_ms", time.Since(s.start).Milliseconds(),
		"ts", time.Now().UnixMilli(),
	)
	if !attrHasKey(fields, "outcome") {
		fields = append(fields, "outcome", outcome)
	}
	if err != nil {
		fields = append(fields, "err", err)
		s.logger.Warn(s.event+".done", fields...)
		return
	}
	s.logger.Info(s.event+".done", fields...)
}

func attrHasKey(attrs []any, key string) bool {
	for i := 0; i+1 < len(attrs); i += 2 {
		k, ok := attrs[i].(string)
		if ok && k == key {
			return true
		}
	}
	return false
}
