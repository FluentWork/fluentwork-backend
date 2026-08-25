package session

import (
	"github.com/gin-gonic/gin"

	"github.com/FluentWork/fluentwork-backend/internal/account"
	"github.com/FluentWork/fluentwork-backend/internal/apierr"
	"github.com/FluentWork/fluentwork-backend/internal/httpjson"
)

// Handler exposes session HTTP endpoints.
type Handler struct {
	svc      *Service
	accounts *account.Handler
}

// NewHandler constructs session HTTP handlers.
func NewHandler(svc *Service, accounts *account.Handler) *Handler {
	return &Handler{svc: svc, accounts: accounts}
}

// RegisterRoutes mounts B2 session routes under /api/v1.
func RegisterRoutes(rg gin.IRouter, h *Handler) {
	rg.POST("/sessions", h.accounts.RequireAuth(), h.PostCreate)
}

// PostCreate handles POST /sessions.
func (h *Handler) PostCreate(c *gin.Context) {
	userID, ok := c.Get(account.ContextUserIDKey)
	if !ok {
		httpjson.Error(c, apierr.Unauthenticated("missing authenticated user"))
		return
	}
	actorID, ok := userID.(string)
	if !ok || actorID == "" {
		httpjson.Error(c, apierr.Unauthenticated("invalid authenticated user"))
		return
	}

	var req CreateRequest
	if c.Request.ContentLength != 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			httpjson.Error(c, apierr.InvalidArgument("invalid json body"))
			return
		}
	}

	result, err := h.svc.Create(c.Request.Context(), actorID, req)
	if err != nil {
		httpjson.Error(c, err)
		return
	}
	httpjson.OK(c, result)
}
