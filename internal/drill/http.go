package drill

import (
	"crypto/subtle"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/FluentWork/fluentwork-backend/internal/account"
	"github.com/FluentWork/fluentwork-backend/internal/apierr"
	"github.com/FluentWork/fluentwork-backend/internal/httpjson"
)

// Handler exposes drill HTTP endpoints.
type Handler struct {
	svc      *Service
	accounts *account.Handler
}

// NewHandler constructs drill HTTP handlers.
func NewHandler(svc *Service, accounts *account.Handler) *Handler {
	return &Handler{svc: svc, accounts: accounts}
}

// RegisterRoutes mounts the drill endpoints under /api/v1.
func RegisterRoutes(rg gin.IRouter, h *Handler) {
	if h == nil || h.accounts == nil {
		return
	}
	rg.GET("/drill/round", h.accounts.RequireAuth(), h.GetRound)
	rg.POST("/drill/judge", h.accounts.RequireAuth(), h.PostJudge)
	rg.POST("/drill/appeal", h.accounts.RequireAuth(), h.PostAppeal)
}

// GetRound handles GET /api/v1/drill/round.
func (h *Handler) GetRound(c *gin.Context) {
	userID, ok := actorID(c)
	if !ok {
		return
	}
	size, _ := strconv.Atoi(c.Query("size"))
	result, err := h.svc.Round(c.Request.Context(), userID, size)
	if err != nil {
		httpjson.Error(c, err)
		return
	}
	httpjson.OK(c, result)
}

// PostJudge handles POST /api/v1/drill/judge.
func (h *Handler) PostJudge(c *gin.Context) {
	userID, ok := actorID(c)
	if !ok {
		return
	}
	var req JudgeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpjson.Error(c, apierr.InvalidArgument("invalid json body"))
		return
	}
	result, err := h.svc.Judge(c.Request.Context(), userID, req)
	if err != nil {
		httpjson.Error(c, err)
		return
	}
	httpjson.OK(c, result)
}

// RegisterInternalRoutes mounts the operator-facing drill endpoints under
// /internal/v1. These are read-only views over one learner's data, used for
// content and difficulty work — not client API.
func RegisterInternalRoutes(rg gin.IRouter, h *Handler, expectedToken string) {
	if h == nil {
		return
	}
	rg.GET("/drill/stuck-map", requireInternalToken(expectedToken), h.GetStuckMap)
}

// GetStuckMap handles GET /internal/v1/drill/stuck-map (86_ M4).
func (h *Handler) GetStuckMap(c *gin.Context) {
	days, _ := strconv.Atoi(c.Query("days"))
	result, err := h.svc.StuckMap(c.Request.Context(), c.Query("user_id"), days)
	if err != nil {
		httpjson.Error(c, err)
		return
	}
	httpjson.OK(c, result)
}

// requireInternalToken mirrors the other modules' internal auth.
func requireInternalToken(expected string) gin.HandlerFunc {
	expected = strings.TrimSpace(expected)
	return func(c *gin.Context) {
		if expected == "" {
			httpjson.Error(c, apierr.Internal("internal API token is not configured"))
			return
		}
		got := strings.TrimSpace(c.GetHeader("X-Internal-Token"))
		if got == "" || len(got) != len(expected) || subtle.ConstantTimeCompare([]byte(got), []byte(expected)) != 1 {
			httpjson.Error(c, apierr.Unauthenticated("invalid internal token"))
			return
		}
		c.Next()
	}
}

// PostAppeal handles POST /api/v1/drill/appeal — E2's 一键申诉.
func (h *Handler) PostAppeal(c *gin.Context) {
	userID, ok := actorID(c)
	if !ok {
		return
	}
	var req AppealRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpjson.Error(c, apierr.InvalidArgument("invalid json body"))
		return
	}
	result, err := h.svc.Appeal(c.Request.Context(), userID, req)
	if err != nil {
		httpjson.Error(c, err)
		return
	}
	httpjson.OK(c, result)
}

func actorID(c *gin.Context) (string, bool) {
	userID, ok := c.Get(account.ContextUserIDKey)
	if !ok {
		httpjson.Error(c, apierr.Unauthenticated("missing authenticated user"))
		return "", false
	}
	id, ok := userID.(string)
	if !ok || strings.TrimSpace(id) == "" {
		httpjson.Error(c, apierr.Unauthenticated("invalid authenticated user"))
		return "", false
	}
	return id, true
}
