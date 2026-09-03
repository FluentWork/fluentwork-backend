package corpus

import (
	"crypto/subtle"
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/FluentWork/fluentwork-backend/internal/apierr"
	"github.com/FluentWork/fluentwork-backend/internal/httpjson"
)

const internalTokenHeader = "X-Internal-Token"

// RegisterInternalRoutes mounts gateway-facing internal routes under /internal/v1.
// The voice-gateway reads the caller's phrase blocks here so B12 hit detection
// uses the same corpus source of truth as the app-server (works with both the
// in-memory dev store and MySQL).
func RegisterInternalRoutes(rg gin.IRouter, h *Handler, expectedToken string) {
	rg.GET("/corpus/blocks", requireInternalToken(expectedToken), h.ListBlocksInternal)
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

// ListBlocksInternal returns one user's phrase blocks for the voice-gateway's
// B12 hit detector. user_id is required; limit defaults to SourceCandidateLimit.
func (h *Handler) ListBlocksInternal(c *gin.Context) {
	userID := strings.TrimSpace(c.Query("user_id"))
	if userID == "" {
		httpjson.Error(c, apierr.InvalidArgument("user_id is required"))
		return
	}
	limit, _ := strconv.Atoi(c.Query("limit"))
	if limit <= 0 || limit > SourceCandidateLimit {
		limit = SourceCandidateLimit
	}
	result, err := h.svc.ListBlocks(c.Request.Context(), ListBlocksRequest{
		UserID: userID,
		Limit:  limit,
	})
	if err != nil {
		httpjson.Error(c, err)
		return
	}
	httpjson.OK(c, result)
}
