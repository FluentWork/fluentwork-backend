package account

import (
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/FluentWork/fluentwork-backend/internal/apierr"
	"github.com/FluentWork/fluentwork-backend/internal/httpjson"
)

// ContextUserIDKey is the gin key for the authenticated user id.
const ContextUserIDKey = "user_id"

// ContextIsGuestKey is the gin key for whether the authenticated user is a guest.
const ContextIsGuestKey = "is_guest"

// Handler exposes account HTTP endpoints.
type Handler struct {
	svc *Service
}

// NewHandler constructs account HTTP handlers.
func NewHandler(svc *Service) *Handler {
	return &Handler{svc: svc}
}

// RegisterRoutes mounts B1 account routes under /api/v1.
func RegisterRoutes(rg gin.IRouter, h *Handler) {
	rg.POST("/auth/guest", h.PostGuest)
	rg.POST("/account/merge", h.RequireRegistered(), h.PostMerge)
}

// RequireAuth rejects missing or invalid bearer tokens. Guests are allowed.
func (h *Handler) RequireAuth() gin.HandlerFunc {
	return h.requireAuth(false)
}

// RequireRegistered rejects missing, invalid, or guest tokens.
func (h *Handler) RequireRegistered() gin.HandlerFunc {
	return h.requireAuth(true)
}

func (h *Handler) requireAuth(registeredOnly bool) gin.HandlerFunc {
	return func(c *gin.Context) {
		header := c.GetHeader("Authorization")
		parts := strings.SplitN(header, " ", 2)
		if len(parts) != 2 || !strings.EqualFold(parts[0], "Bearer") {
			httpjson.Error(c, apierr.Unauthenticated("missing bearer token"))
			return
		}
		token := strings.TrimSpace(parts[1])
		if token == "" {
			httpjson.Error(c, apierr.Unauthenticated("missing bearer token"))
			return
		}
		user, err := h.svc.Authenticate(c.Request.Context(), token)
		if err != nil {
			httpjson.Error(c, err)
			return
		}
		if registeredOnly && user.IsGuest {
			httpjson.Error(c, apierr.PermissionDenied("registered account required"))
			return
		}
		c.Set(ContextUserIDKey, user.ID)
		c.Set(ContextIsGuestKey, user.IsGuest)
		c.Next()
	}
}

// PostGuest handles POST /auth/guest.
func (h *Handler) PostGuest(c *gin.Context) {
	var req GuestRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpjson.Error(c, apierr.InvalidArgument("invalid json body"))
		return
	}
	result, err := h.svc.IssueGuest(c.Request.Context(), req.DeviceID)
	if err != nil {
		httpjson.Error(c, err)
		return
	}
	httpjson.OK(c, result)
}

// PostMerge handles POST /account/merge.
func (h *Handler) PostMerge(c *gin.Context) {
	userID, ok := c.Get(ContextUserIDKey)
	if !ok {
		httpjson.Error(c, apierr.Unauthenticated("missing authenticated user"))
		return
	}
	actorID, ok := userID.(string)
	if !ok || actorID == "" {
		httpjson.Error(c, apierr.Unauthenticated("invalid authenticated user"))
		return
	}
	var req MergeRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpjson.Error(c, apierr.InvalidArgument("invalid json body"))
		return
	}
	result, err := h.svc.Merge(c.Request.Context(), actorID, req.DeviceID)
	if err != nil {
		httpjson.Error(c, err)
		return
	}
	httpjson.OK(c, result)
}
