package account

import (
	"crypto/subtle"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/FluentWork/fluentwork-backend/internal/apierr"
	"github.com/FluentWork/fluentwork-backend/internal/httpjson"
)

const internalTokenHeader = "X-Internal-Token"

// RegisterInternalRoutes mounts POST /internal/v1/support/undelete-user.
func RegisterInternalRoutes(rg gin.IRouter, h *Handler, expectedToken string) {
	if h == nil || h.privacy == nil {
		return
	}
	rg.POST("/support/undelete-user", requireInternalToken(expectedToken), h.PostUndeleteUser)
}

// PostUndeleteUser handles support restore. Per 48 §2.8.
func (h *Handler) PostUndeleteUser(c *gin.Context) {
	if h.privacy == nil {
		httpjson.Error(c, apierr.Unavailable("privacy service is not configured"))
		return
	}
	var req UndeleteUserRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpjson.Error(c, apierr.InvalidArgument("invalid json body"))
		return
	}
	result, err := h.privacy.UndeleteUser(c.Request.Context(), req.UserID, req.Actor, req.Reason)
	if err != nil {
		httpjson.Error(c, err)
		return
	}
	httpjson.OK(c, result)
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
