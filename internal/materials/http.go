package materials

import (
	"github.com/gin-gonic/gin"

	"github.com/FluentWork/fluentwork-backend/internal/account"
	"github.com/FluentWork/fluentwork-backend/internal/apierr"
	"github.com/FluentWork/fluentwork-backend/internal/httpjson"
)

// Handler exposes B21 material HTTP endpoints.
type Handler struct {
	svc      *Service
	accounts *account.Handler
}

// NewHandler constructs material handlers.
func NewHandler(svc *Service, accounts *account.Handler) *Handler {
	return &Handler{svc: svc, accounts: accounts}
}

// RegisterRoutes mounts POST /materials and GET /materials/:id.
func RegisterRoutes(rg gin.IRouter, h *Handler) {
	if h == nil || h.accounts == nil || h.svc == nil {
		return
	}
	rg.POST("/materials", h.accounts.RequireAuth(), h.PostMaterial)
	rg.GET("/materials/:id", h.accounts.RequireAuth(), h.GetMaterial)
}

// PostMaterial handles POST /api/v1/materials.
func (h *Handler) PostMaterial(c *gin.Context) {
	userID, ok := actorID(c)
	if !ok {
		return
	}
	var req CreateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpjson.Error(c, apierr.InvalidArgument("invalid json body"))
		return
	}
	result, err := h.svc.Create(c.Request.Context(), userID, req)
	if err != nil {
		httpjson.Error(c, err)
		return
	}
	c.JSON(202, result)
}

// GetMaterial handles GET /api/v1/materials/:id.
func (h *Handler) GetMaterial(c *gin.Context) {
	userID, ok := actorID(c)
	if !ok {
		return
	}
	m, err := h.svc.Get(c.Request.Context(), userID, c.Param("id"))
	if err != nil {
		httpjson.Error(c, err)
		return
	}
	httpjson.OK(c, m)
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
