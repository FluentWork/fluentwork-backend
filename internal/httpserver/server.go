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
	"github.com/FluentWork/fluentwork-backend/internal/aicost"
	"github.com/FluentWork/fluentwork-backend/internal/apierr"
	"github.com/FluentWork/fluentwork-backend/internal/config"
	"github.com/FluentWork/fluentwork-backend/internal/content"
	"github.com/FluentWork/fluentwork-backend/internal/content/tts"
	"github.com/FluentWork/fluentwork-backend/internal/corpus"
	"github.com/FluentWork/fluentwork-backend/internal/drill"
	"github.com/FluentWork/fluentwork-backend/internal/httpjson"
	"github.com/FluentWork/fluentwork-backend/internal/session"
)

var ginOnce sync.Once

// Server is the HTTP application.
type Server struct {
	engine *gin.Engine
}

// New constructs the Gin engine with health checks, account, and session routes.
// costHandler is optional — when nil, the /internal/v1/ai-cost-logs route is not
// mounted (mirrors the optional pattern used for corpusHandler / contentHandler).
func New(
	cfg config.Config,
	logger *slog.Logger,
	accounts *account.Handler,
	corpusHandler *corpus.Handler,
	contentHandler *content.Handler,
	sessions *session.Handler,
	costHandler *aicost.Handler,
	ttsHandler *tts.Handler,
	drillHandler *drill.Handler,
	ready func(context.Context) error,
) *Server {
	ginOnce.Do(func() {
		gin.SetMode(gin.ReleaseMode)
	})
	engine := gin.New()
	engine.Use(withLogger(logger), RequestID(), Recover(logger), AccessLog(logger))
	engine.GET("/healthz", liveness)
	engine.GET("/readyz", readiness(ready))
	engine.GET("/metrics", serveMetrics)
	engine.GET("/", discovery)
	engine.GET("/openapi.yaml", serveOpenAPI)
	engine.GET("/openapi/v1.yaml", serveOpenAPI)
	apiGroup := engine.Group("/api/v1")
	if accounts != nil {
		account.RegisterRoutes(apiGroup, accounts)
		account.RegisterInternalRoutes(engine.Group("/internal/v1"), accounts, cfg.InternalAPIToken)
	}
	if corpusHandler != nil {
		corpus.RegisterRoutes(apiGroup, corpusHandler)
		corpus.RegisterInternalRoutes(engine.Group("/internal/v1"), corpusHandler, cfg.InternalAPIToken)
	}
	if contentHandler != nil {
		content.RegisterRoutes(apiGroup, contentHandler)
	}
	if sessions != nil {
		session.RegisterRoutes(apiGroup, sessions)
		session.RegisterInternalRoutes(engine.Group("/internal/v1"), sessions, cfg.InternalAPIToken)
	}
	if costHandler != nil {
		aicost.RegisterInternalRoutes(engine.Group("/internal/v1"), costHandler, cfg.InternalAPIToken)
	}
	if ttsHandler != nil {
		tts.RegisterInternalRoutes(engine.Group("/internal/v1"), ttsHandler, cfg.InternalAPIToken)
	}
	if drillHandler != nil {
		drill.RegisterRoutes(apiGroup, drillHandler)
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
		"metrics":    "/metrics",
		"tts":        "/internal/v1/tts/synthesize",
		"hits":       "/internal/v1/voicegateway/hits",
		"drill":      "/api/v1/drill/round",
		"privacy":    "/api/v1/account/data",
	})
}

func serveMetrics(c *gin.Context) {
	c.Header("Cache-Control", "no-cache")
	c.Data(http.StatusOK, "text/plain; version=0.0.4; charset=utf-8", []byte(
		tts.PrometheusMetrics()+corpus.PrometheusMetrics()+drill.PrometheusMetrics()+account.PrivacyPrometheusMetrics(),
	))
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
