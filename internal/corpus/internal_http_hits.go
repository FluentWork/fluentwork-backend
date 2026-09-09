package corpus

import (
	"strconv"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/FluentWork/fluentwork-backend/internal/apierr"
	"github.com/FluentWork/fluentwork-backend/internal/httpjson"
)

// RegisterHitsInternalRoutes mounts B7 hit ledger routes under /internal/v1.
func RegisterHitsInternalRoutes(rg gin.IRouter, h *Handler, expectedToken string) {
	if h == nil {
		return
	}
	mw := requireInternalToken(expectedToken)
	rg.POST("/voicegateway/hits", mw, h.PostVoiceGatewayHits)
	rg.GET("/sessions/:session_id/recent-hits", mw, h.GetRecentHits)
}

// RecordHitsRequest is the body of POST /internal/v1/voicegateway/hits.
type RecordHitsRequest struct {
	UserID    string           `json:"user_id"`
	SessionID string           `json:"session_id"`
	TurnID    string           `json:"turn_id"`
	Hits      []RecordHitsItem `json:"hits"`
}

// RecordHitsItem is one detected block in a voicegateway hits payload.
type RecordHitsItem struct {
	BlockID      string `json:"block_id"`
	DetectedAtMs int64  `json:"detected_at_ms"`
	TurnID       string `json:"turn_id"`
}

// RecordHitsResponse is returned after ledger UPSERT.
type RecordHitsResponse struct {
	RecordedCount int `json:"recorded_count"`
}

// PostVoiceGatewayHits handles POST /internal/v1/voicegateway/hits.
func (h *Handler) PostVoiceGatewayHits(c *gin.Context) {
	if h == nil || h.svc == nil {
		httpjson.Error(c, apierr.Internal("corpus service is not configured"))
		return
	}
	var req RecordHitsRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpjson.Error(c, apierr.InvalidArgument("invalid json body"))
		return
	}
	hits := make([]Hit, 0, len(req.Hits))
	for _, item := range req.Hits {
		hits = append(hits, Hit{
			BlockID:      item.BlockID,
			TurnID:       item.TurnID,
			DetectedAtMs: item.DetectedAtMs,
		})
	}
	n, err := h.svc.RecordHits(c.Request.Context(), req.UserID, req.SessionID, req.TurnID, hits)
	if err != nil {
		httpjson.Error(c, err)
		return
	}
	httpjson.OK(c, RecordHitsResponse{RecordedCount: n})
}

// GetRecentHits handles GET /internal/v1/sessions/:session_id/recent-hits.
func (h *Handler) GetRecentHits(c *gin.Context) {
	if h == nil || h.svc == nil {
		httpjson.Error(c, apierr.Internal("corpus service is not configured"))
		return
	}
	lookback := DefaultLookbackTurns
	if raw := strings.TrimSpace(c.Query("lookback_turns")); raw != "" {
		n, err := strconv.Atoi(raw)
		if err != nil {
			httpjson.Error(c, apierr.InvalidArgument("lookback_turns is invalid"))
			return
		}
		lookback = n
	}
	minScore := DefaultMinScore
	if raw := strings.TrimSpace(c.Query("min_score")); raw != "" {
		v, err := strconv.ParseFloat(raw, 64)
		if err != nil {
			httpjson.Error(c, apierr.InvalidArgument("min_score is invalid"))
			return
		}
		minScore = v
	}
	result, err := h.svc.RecentHits(c.Request.Context(), c.Param("session_id"), lookback, minScore)
	if err != nil {
		httpjson.Error(c, err)
		return
	}
	httpjson.OK(c, result)
}
