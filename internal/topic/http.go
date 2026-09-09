package topic

import (
	"github.com/gin-gonic/gin"

	"github.com/FluentWork/fluentwork-backend/internal/account"
	"github.com/FluentWork/fluentwork-backend/internal/apierr"
	"github.com/FluentWork/fluentwork-backend/internal/httpjson"
)

// Handler exposes B23 topic HTTP endpoints. Per 48 §1.7.
type Handler struct {
	svc      *Service
	accounts *account.Handler
}

// NewHandler constructs topic handlers.
func NewHandler(svc *Service, accounts *account.Handler) *Handler {
	return &Handler{svc: svc, accounts: accounts}
}

// RegisterRoutes mounts GET /topic-cards and POST /topic-cards/:id/checkin.
func RegisterRoutes(rg gin.IRouter, h *Handler) {
	if h == nil || h.accounts == nil || h.svc == nil {
		return
	}
	rg.GET("/topic-cards", h.accounts.RequireAuth(), h.GetCards)
	rg.POST("/topic-cards/:id/checkin", h.accounts.RequireAuth(), h.PostCheckin)
}

// GetCards handles GET /api/v1/topic-cards.
func (h *Handler) GetCards(c *gin.Context) {
	userID, ok := actorID(c)
	if !ok {
		return
	}
	result, err := h.svc.ListToday(c.Request.Context(), userID)
	if err != nil {
		httpjson.Error(c, err)
		return
	}
	httpjson.OK(c, result)
}

// PostCheckin handles POST /api/v1/topic-cards/:id/checkin.
func (h *Handler) PostCheckin(c *gin.Context) {
	userID, ok := actorID(c)
	if !ok {
		return
	}
	var req CheckinRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpjson.Error(c, apierr.InvalidArgument("invalid json body"))
		return
	}
	result, err := h.svc.Checkin(c.Request.Context(), userID, c.Param("id"), req.Reflection)
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
	if !ok || id == "" {
		httpjson.Error(c, apierr.Unauthenticated("invalid authenticated user"))
		return "", false
	}
	return id, true
}
