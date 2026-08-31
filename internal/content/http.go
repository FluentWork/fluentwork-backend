package content

import (
	"github.com/gin-gonic/gin"

	"github.com/FluentWork/fluentwork-backend/internal/account"
	"github.com/FluentWork/fluentwork-backend/internal/apierr"
	"github.com/FluentWork/fluentwork-backend/internal/httpjson"
)

// Handler exposes content HTTP endpoints.
type Handler struct {
	svc      *Service
	accounts *account.Handler
}

// NewHandler constructs content HTTP handlers.
func NewHandler(svc *Service, accounts *account.Handler) *Handler {
	return &Handler{svc: svc, accounts: accounts}
}

// RegisterRoutes mounts content routes under /api/v1.
func RegisterRoutes(rg gin.IRouter, h *Handler) {
	rg.GET("/daily-reads/today", h.accounts.RequireAuth(), h.GetToday)
	rg.POST("/daily-reads/:id/follow-read", h.accounts.RequireAuth(), h.PostFollowRead)
}

// GetToday handles GET /daily-reads/today.
func (h *Handler) GetToday(c *gin.Context) {
	actorID, ok := mustActorID(c)
	if !ok {
		return
	}
	result, err := h.svc.GetToday(c.Request.Context(), actorID)
	if err != nil {
		httpjson.Error(c, err)
		return
	}
	httpjson.OK(c, result)
}

// PostFollowRead handles POST /daily-reads/:id/follow-read.
func (h *Handler) PostFollowRead(c *gin.Context) {
	actorID, ok := mustActorID(c)
	if !ok {
		return
	}
	var req FollowReadRequest
	if c.Request.ContentLength != 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			httpjson.Error(c, apierr.InvalidArgument("invalid json body"))
			return
		}
	}
	result, err := h.svc.FollowRead(c.Request.Context(), actorID, c.Param("id"), req)
	if err != nil {
		httpjson.Error(c, err)
		return
	}
	httpjson.OK(c, result)
}

func mustActorID(c *gin.Context) (string, bool) {
	userID, ok := c.Get(account.ContextUserIDKey)
	if !ok {
		httpjson.Error(c, apierr.Unauthenticated("missing authenticated user"))
		return "", false
	}
	actorID, ok := userID.(string)
	if !ok || actorID == "" {
		httpjson.Error(c, apierr.Unauthenticated("invalid authenticated user"))
		return "", false
	}
	return actorID, true
}
