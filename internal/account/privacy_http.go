package account

import (
	"github.com/gin-gonic/gin"

	"github.com/FluentWork/fluentwork-backend/internal/apierr"
	"github.com/FluentWork/fluentwork-backend/internal/httpjson"
)

// SetPrivacy wires A4 handlers onto the account HTTP surface.
func (h *Handler) SetPrivacy(privacy *PrivacyService) {
	if h == nil {
		return
	}
	h.privacy = privacy
}

// DeleteData handles DELETE /api/v1/account/data. Per 48 §1.1.6.
func (h *Handler) DeleteData(c *gin.Context) {
	if h.privacy == nil {
		httpjson.Error(c, apierr.Unavailable("privacy service is not configured"))
		return
	}
	userID, ok := actorID(c)
	if !ok {
		return
	}
	var req DeleteDataRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpjson.Error(c, apierr.InvalidArgument("invalid json body"))
		return
	}
	result, err := h.privacy.DeleteAllData(c.Request.Context(), userID, req.ConfirmationCode)
	if err != nil {
		httpjson.Error(c, err)
		return
	}
	httpjson.OK(c, result)
}

// PostExport handles POST /api/v1/account/export. Per 48 §1.1.7.
func (h *Handler) PostExport(c *gin.Context) {
	if h.privacy == nil {
		httpjson.Error(c, apierr.Unavailable("privacy service is not configured"))
		return
	}
	userID, ok := actorID(c)
	if !ok {
		return
	}
	var req ExportDataRequest
	if c.Request.ContentLength != 0 {
		if err := c.ShouldBindJSON(&req); err != nil {
			httpjson.Error(c, apierr.InvalidArgument("invalid json body"))
			return
		}
	}
	result, err := h.privacy.EnqueueExport(c.Request.Context(), userID, req.EmailTo)
	if err != nil {
		httpjson.Error(c, err)
		return
	}
	httpjson.OK(c, result)
}

func actorID(c *gin.Context) (string, bool) {
	userID, ok := c.Get(ContextUserIDKey)
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
