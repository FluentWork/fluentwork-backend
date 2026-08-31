// Package httpserver wires the app-server HTTP surface.
package httpserver

import (
	"context"
	"log/slog"
	"net/http"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/FluentWork/fluentwork-backend/api"
	"github.com/FluentWork/fluentwork-backend/internal/account"
	"github.com/FluentWork/fluentwork-backend/internal/apierr"
	"github.com/FluentWork/fluentwork-backend/internal/config"
	"github.com/FluentWork/fluentwork-backend/internal/corpus"
	"github.com/FluentWork/fluentwork-backend/internal/httpjson"
	"github.com/FluentWork/fluentwork-backend/internal/session"
)

var ginOnce sync.Once

// Server is the HTTP application.
type Server struct {
	engine *gin.Engine
}

// New constructs the Gin engine with health checks, account, and session routes.
func New(
	cfg config.Config,
	logger *slog.Logger,
	accounts *account.Handler,
	corpusHandler *corpus.Handler,
	sessions *session.Handler,
	ready func(context.Context) error,
) *Server {
	ginOnce.Do(func() {
		gin.SetMode(gin.ReleaseMode)
	})
	engine := gin.New()
	engine.Use(withLogger(logger), RequestID(), Recover(logger), AccessLog(logger))
	engine.GET("/healthz", liveness)
	engine.GET("/readyz", readiness(ready))
	engine.GET("/", discovery)
	engine.GET("/openapi.yaml", serveOpenAPI)
	engine.GET("/openapi/v1.yaml", serveOpenAPI)
	apiGroup := engine.Group("/api/v1")
	if accounts != nil {
		account.RegisterRoutes(apiGroup, accounts)
	}
	if corpusHandler != nil {
		corpus.RegisterRoutes(apiGroup, corpusHandler)
	}
	if sessions != nil {
		session.RegisterRoutes(apiGroup, sessions)
		session.RegisterInternalRoutes(engine.Group("/internal/v1"), sessions, cfg.InternalAPIToken)
	}
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

func discovery(c *gin.Context) {
	httpjson.OK(c, gin.H{
		"service":    "app-server",
		"api_prefix": "/api/v1",
		"openapi":    "/openapi.yaml",
		"healthz":    "/healthz",
		"readyz":     "/readyz",
	})
}

func serveOpenAPI(c *gin.Context) {
	c.Header("Cache-Control", "no-cache")
	c.Data(http.StatusOK, "application/yaml; charset=utf-8", api.OpenAPIV1)
}

func withLogger(logger *slog.Logger) gin.HandlerFunc {
	return func(c *gin.Context) {
		c.Set(httpjson.LoggerContextKey, logger)
		c.Next()
	}
}
