package drill

import (
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

// RegisterRoutes mounts GET /drill/round and POST /drill/judge under /api/v1.
func RegisterRoutes(rg gin.IRouter, h *Handler) {
	if h == nil || h.accounts == nil {
		return
	}
	rg.GET("/drill/round", h.accounts.RequireAuth(), h.GetRound)
	rg.POST("/drill/judge", h.accounts.RequireAuth(), h.PostJudge)
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
