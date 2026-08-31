package corpus

import (
	"strconv"

	"github.com/gin-gonic/gin"

	"github.com/FluentWork/fluentwork-backend/internal/account"
	"github.com/FluentWork/fluentwork-backend/internal/apierr"
	"github.com/FluentWork/fluentwork-backend/internal/httpjson"
)

// Handler exposes corpus HTTP endpoints.
type Handler struct {
	svc      *Service
	accounts *account.Handler
}

// NewHandler constructs corpus HTTP handlers.
func NewHandler(svc *Service, accounts *account.Handler) *Handler {
	return &Handler{svc: svc, accounts: accounts}
}

// RegisterRoutes mounts corpus routes under /api/v1.
func RegisterRoutes(rg gin.IRouter, h *Handler) {
	rg.GET("/corpus/blocks", h.accounts.RequireAuth(), h.GetBlocks)
	rg.PUT("/corpus/blocks/:id", h.accounts.RequireAuth(), h.PutBlock)
	rg.DELETE("/corpus/blocks/:id", h.accounts.RequireAuth(), h.DeleteBlock)
	rg.POST("/corpus/blocks/:id/favorite", h.accounts.RequireAuth(), h.PostFavorite)
	rg.POST("/corpus/blocks/batch-accept", h.accounts.RequireAuth(), h.PostBatchAccept)
}

// GetBlocks handles GET /corpus/blocks.
func (h *Handler) GetBlocks(c *gin.Context) {
	actorID, ok := mustActorID(c)
	if !ok {
		return
	}
	limit, _ := strconv.Atoi(c.Query("limit"))
	result, err := h.svc.ListBlocks(c.Request.Context(), ListBlocksRequest{
		UserID:       actorID,
		SceneTag:     c.Query("scene"),
		FunctionTag:  c.Query("func"),
		Keyword:      c.Query("kw"),
		Cursor:       c.Query("cursor"),
		UpdatedAfter: c.Query("updated_after"),
		Limit:        limit,
		FavoriteOnly: c.Query("favorite_only") == "true",
	})
	if err != nil {
		httpjson.Error(c, err)
		return
	}
	httpjson.OK(c, result)
}

// PutBlock handles PUT /corpus/blocks/:id.
func (h *Handler) PutBlock(c *gin.Context) {
	actorID, ok := mustActorID(c)
	if !ok {
		return
	}
	var req UpdateBlockRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpjson.Error(c, apierr.InvalidArgument("invalid json body"))
		return
	}
	result, err := h.svc.UpdateBlock(c.Request.Context(), actorID, c.Param("id"), req)
	if err != nil {
		httpjson.Error(c, err)
		return
	}
	httpjson.OK(c, result)
}

// DeleteBlock handles DELETE /corpus/blocks/:id.
func (h *Handler) DeleteBlock(c *gin.Context) {
	actorID, ok := mustActorID(c)
	if !ok {
		return
	}
	if err := h.svc.DeleteBlock(c.Request.Context(), actorID, c.Param("id")); err != nil {
		httpjson.Error(c, err)
		return
	}
	httpjson.OK(c, gin.H{"deleted": true})
}

// PostFavorite handles POST /corpus/blocks/:id/favorite.
func (h *Handler) PostFavorite(c *gin.Context) {
	actorID, ok := mustActorID(c)
	if !ok {
		return
	}
	var req FavoriteBlockRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpjson.Error(c, apierr.InvalidArgument("invalid json body"))
		return
	}
	result, err := h.svc.SetFavorite(c.Request.Context(), actorID, c.Param("id"), req)
	if err != nil {
		httpjson.Error(c, err)
		return
	}
	httpjson.OK(c, result)
}

// PostBatchAccept handles POST /corpus/blocks/batch-accept.
func (h *Handler) PostBatchAccept(c *gin.Context) {
	actorID, ok := mustActorID(c)
	if !ok {
		return
	}
	var req BatchAcceptRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpjson.Error(c, apierr.InvalidArgument("invalid json body"))
		return
	}
	result, err := h.svc.BatchAccept(c.Request.Context(), actorID, req)
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
