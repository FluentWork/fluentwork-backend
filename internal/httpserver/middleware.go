package httpserver

import (
	"log/slog"
	"net/http"
	"time"

	"github.com/gin-gonic/gin"
	"github.com/google/uuid"

	"github.com/FluentWork/fluentwork-backend/internal/account"
	"github.com/FluentWork/fluentwork-backend/internal/apierr"
	"github.com/FluentWork/fluentwork-backend/internal/httpjson"
)

const requestIDHeader = "X-Request-ID"

// RequestID copies or generates a request id for logs and error envelopes.
func RequestID() gin.HandlerFunc {
	return func(c *gin.Context) {
		id := c.GetHeader(requestIDHeader)
		if id == "" || len(id) > 64 {
			id = uuid.NewString()
		}
		c.Set(httpjson.RequestIDContextKey, id)
		if value, ok := c.Get(httpjson.LoggerContextKey); ok {
			if logger, ok := value.(*slog.Logger); ok && logger != nil {
				c.Set(httpjson.LoggerContextKey, logger.With("request_id", id))
			}
		}
		c.Header(requestIDHeader, id)
		c.Next()
	}
}

// Recover turns panics into INTERNAL errors.
func Recover(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		defer func() {
			if rec := recover(); rec != nil {
				if rec == http.ErrAbortHandler {
					panic(rec)
				}
				if logger != nil {
					logger.Error("panic recovered", "panic", rec, "request_id", httpjson.RequestID(c))
				}
				httpjson.Error(c, apierr.Internal("internal error"))
			}
		}()
		c.Next()
	}
}

// AccessLog writes a structured request log line.
func AccessLog(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		start := time.Now()
		c.Next()
		lg := logger
		if value, ok := c.Get(httpjson.LoggerContextKey); ok {
			if scoped, ok := value.(*slog.Logger); ok && scoped != nil {
				lg = scoped
			}
		}
		if lg == nil {
			return
		}
		attrs := []any{
			"method", c.Request.Method,
			"path", c.Request.URL.Path,
			"route", c.FullPath(),
			"status", c.Writer.Status(),
			"duration_ms", time.Since(start).Milliseconds(),
		}
		if value, ok := c.Get(account.ContextUserIDKey); ok {
			attrs = append(attrs, "user_id", value)
		}
		if value, ok := c.Get(account.ContextIsGuestKey); ok {
			attrs = append(attrs, "is_guest", value)
		}
		lg.Info("http", attrs...)
	}
}
