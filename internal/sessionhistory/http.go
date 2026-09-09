package sessionhistory

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/FluentWork/fluentwork-backend/internal/account"
	"github.com/FluentWork/fluentwork-backend/internal/apierr"
	"github.com/FluentWork/fluentwork-backend/internal/httpjson"
)

// Handler exposes B24 history HTTP endpoints.
type Handler struct {
	svc      *Service
	accounts *account.Handler
}

// NewHandler constructs history HTTP handlers.
func NewHandler(svc *Service, accounts *account.Handler) *Handler {
	return &Handler{svc: svc, accounts: accounts}
}

// RegisterRoutes mounts GET /sessions and GET /sessions/:id. Per 48 §1.3.2.
func RegisterRoutes(rg gin.IRouter, h *Handler) {
	if h == nil || h.accounts == nil || h.svc == nil {
		return
	}
	rg.GET("/sessions", h.accounts.RequireAuth(), h.ListSessions)
	rg.GET("/sessions/:id", h.accounts.RequireAuth(), h.GetSessionDetail)
}

// ListSessions handles GET /api/v1/sessions.
func (h *Handler) ListSessions(c *gin.Context) {
	userID, ok := actorID(c)
	if !ok {
		return
	}
	size, _ := strconv.Atoi(c.Query("size"))
	page, err := h.svc.List(c.Request.Context(), userID, c.Query("cursor"), size)
	if err != nil {
		httpjson.Error(c, err)
		return
	}
	httpjson.OK(c, page)
}

// GetSessionDetail handles GET /api/v1/sessions/:id.
func (h *Handler) GetSessionDetail(c *gin.Context) {
	userID, ok := actorID(c)
	if !ok {
		return
	}
	detail, err := h.svc.GetDetail(c.Request.Context(), userID, c.Param("id"))
	if err != nil {
		httpjson.Error(c, err)
		return
	}
	httpjson.OK(c, detail)
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
