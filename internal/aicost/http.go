package aicost

import (
	"crypto/subtle"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/FluentWork/fluentwork-backend/internal/apierr"
	"github.com/FluentWork/fluentwork-backend/internal/httpjson"
)

const internalTokenHeader = "X-Internal-Token"

// ListRecent default / max page size for the HTTP query endpoint. The MemoryStore
// and MySQLStore both clamp internally to 500 as well; the handler clamps here
// too so the HTTP surface advertises a stable contract independent of the
// underlying store implementation.
const (
	defaultListLimit = 50
	maxListLimit     = 500
)

// Handler exposes aicost HTTP endpoints. Currently internal-only — used by
// smoke-review-ready for evidence collection and by ops for ledger inspection.
// No /api/v1 user-facing routes are mounted: writes must stay in the review
// worker transaction path so cost ledger rows commit atomically with review_json.
type Handler struct {
	svc *Service
}

// NewHandler constructs an aicost HTTP handler.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// RegisterInternalRoutes mounts the aicost HTTP routes under /internal/v1.
// Routes:
//
//	GET /internal/v1/ai-cost-logs?user_id=&limit=
//
// `user_id` is optional — when empty the endpoint returns rows across all users.
// `limit` defaults to 50 and is capped at 500 (matching MemoryStore/MySQLStore
// internals so the HTTP contract stays stable).
//
// Auth: requires the `X-Internal-Token` header to match the configured
// INTERNAL_API_TOKEN. There is no user-scoped auth — this endpoint is for
// internal tooling (smoke harness, ops scripts, future admin UI).
func RegisterInternalRoutes(rg gin.IRouter, h *Handler, expectedToken string) {
	rg.GET("/ai-cost-logs", requireInternalToken(expectedToken), h.ListRecentInternal)
}

func requireInternalToken(expected string) gin.HandlerFunc {
	expected = strings.TrimSpace(expected)
	return func(c *gin.Context) {
		if expected == "" {
			httpjson.Error(c, apierr.Internal("internal API token is not configured"))
			return
		}
		got := strings.TrimSpace(c.GetHeader(internalTokenHeader))
		if got == "" || len(got) != len(expected) || subtle.ConstantTimeCompare([]byte(got), []byte(expected)) != 1 {
			httpjson.Error(c, apierr.Unauthenticated("invalid internal token"))
			return
		}
		c.Next()
	}
}

// ListRecentInternal handles GET /internal/v1/ai-cost-logs.
//
// Returns rows oldest → newest within the requested window. The response wraps
// the array under a `logs` key so future fields (pagination cursors, totals)
// can be added without breaking the existing JSON shape.
func (h *Handler) ListRecentInternal(c *gin.Context) {
	userID := strings.TrimSpace(c.Query("user_id"))
	limit, _ := strconv.Atoi(c.Query("limit"))
	if limit <= 0 {
		limit = defaultListLimit
	}
	if limit > maxListLimit {
		limit = maxListLimit
	}
	logs, err := h.svc.ListRecent(c.Request.Context(), userID, limit)
	if err != nil {
		httpjson.Error(c, err)
		return
	}
	httpjson.OK(c, gin.H{"logs": logs})
}
