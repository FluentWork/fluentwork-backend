// Package httpserver wires the app-server HTTP surface.
package httpserver

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/FluentWork/fluentwork-backend/internal/account"
	"github.com/FluentWork/fluentwork-backend/internal/apierr"
	"github.com/FluentWork/fluentwork-backend/internal/config"
	"github.com/FluentWork/fluentwork-backend/internal/httpjson"
)

var ginOnce sync.Once

// Server is the HTTP application.
type Server struct {
	engine *gin.Engine
}

// New constructs the Gin engine with health checks and account routes.
func New(cfg config.Config, logger *slog.Logger, accounts *account.Handler, ready func(context.Context) error) *Server {
	ginOnce.Do(func() {
		gin.SetMode(gin.ReleaseMode)
	})
	engine := gin.New()
	engine.Use(withLogger(logger), RequestID(), Recover(logger), AccessLog(logger))
	engine.GET("/healthz", liveness)
	engine.GET("/readyz", readiness(ready))
	account.RegisterRoutes(engine.Group("/api/v1"), accounts)
	engine.NoRoute(func(c *gin.Context) {
		httpjson.Error(c, apierr.NotFound("route not found"))
	})
	if logger != nil {
		logger.Info("http routes mounted", "addr", cfg.HTTPAddr)
	}
	return &Server{engine: engine}
}

// Handler exposes the engine as net/http.Handler.
func (s *Server) Handler() http.Handler {
	return s.engine
}

func liveness(c *gin.Context) {
	httpjson.OK(c, gin.H{"status": "ok"})
}

func readiness(ready func(context.Context) error) gin.HandlerFunc {
	return func(c *gin.Context) {
		if ready != nil {
			ctx, cancel := context.WithTimeout(c.Request.Context(), 2*time.Second)
			defer cancel()
			if err := ready(ctx); err != nil {
				httpjson.Error(c, apierr.Unavailable("not ready"))
				return
			}
		}
		httpjson.OK(c, gin.H{"status": "ready"})
	}
}

func withLogger(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set(httpjson.LoggerContextKey, logger)
		c.Next()
	}
}
