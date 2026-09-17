package conversation

import (
	"crypto/subtle"
	"encoding/json"
	"log/slog"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/FluentWork/fluentwork-backend/internal/apierr"
	"github.com/FluentWork/fluentwork-backend/internal/httpjson"
)

// Handler exposes rescue ladder generation to the voice gateway.
//
// The ladder is generated per rung, the moment a learner goes quiet, and the
// gateway holds a 3s budget for it — so this endpoint exists to keep the model,
// its prompt and its cost ledger in app-server, where every other model call
// already lives, instead of handing the gateway its own provider credentials
// and a second, unaccounted spending path.
type Handler struct {
	gen    *RescueGenerator
	logger *slog.Logger
}

// NewHandler constructs the rescue HTTP handler. A nil generator answers
// UNAVAILABLE: an unwired ladder must not look like a model failure.
func NewHandler(gen *RescueGenerator, logger *slog.Logger) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	return &Handler{gen: gen, logger: logger.With("component", "conversation.rescue")}
}

// RegisterInternalRoutes mounts POST /internal/v1/rescue/ladder.
func RegisterInternalRoutes(rg gin.IRouter, h *Handler, expectedToken string) {
	if h == nil {
		return
	}
	rg.POST("/rescue/ladder", requireInternalToken(expectedToken), h.PostLadder)
}

// LadderRequest is one rung request from the gateway.
type LadderRequest struct {
	SessionID string `json:"session_id"`
	UserID    string `json:"user_id"`
	TurnID    string `json:"turn_id"`
	Level     int    `json:"level"`
	// The conversation as the gateway sees it. Only what the generator needs —
	// the gateway holds this state precisely because the model must not.
	LastAIMessage   string `json:"last_ai_message"`
	ScenarioContext string `json:"scenario_context"`
	UserRole        string `json:"user_role"`
	RecentTurns     []Turn `json:"recent_turns"`
}

// LadderResponse is one rung's text.
type LadderResponse struct {
	Level int    `json:"level"`
	Text  string `json:"text"`
}

// PostLadder handles POST /internal/v1/rescue/ladder.
func (h *Handler) PostLadder(c *gin.Context) {
	if h == nil || h.gen == nil {
		httpjson.Error(c, apierr.Unavailable("rescue generator is not configured"))
		return
	}
	var req LadderRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpjson.Error(c, apierr.InvalidArgument("invalid json body"))
		return
	}
	level, ok := ladderLevel(req.Level)
	if !ok {
		httpjson.Error(c, apierr.InvalidArgument("level must be 1, 2 or 3"))
		return
	}
	if strings.TrimSpace(req.SessionID) == "" {
		httpjson.Error(c, apierr.InvalidArgument("session_id is required"))
		return
	}
	text, err := h.gen.GenerateRescue(c.Request.Context(), level, ConversationContext{
		LastAIMessage:   req.LastAIMessage,
		ScenarioContext: req.ScenarioContext,
		UserRole:        req.UserRole,
		RecentTurns:     req.RecentTurns,
	})
	if err != nil {
		// The gateway falls back to its static library on any error, so this is
		// a degraded rung rather than a lost one — say so plainly.
		h.logger.Warn("rescue ladder generation failed",
			"session_id", req.SessionID, "turn_id", req.TurnID, "level", req.Level, "err", err)
		httpjson.Error(c, apierr.Unavailable("rescue ladder generation failed"))
		return
	}
	httpjson.OK(c, LadderResponse{Level: req.Level, Text: text})
}

func ladderLevel(level int) (RescueLevel, bool) {
	switch level {
	case 1:
		return RescueSkeleton, true
	case 2:
		return RescueHint, true
	case 3:
		return RescueComplete, true
	default:
		return 0, false
	}
}

func requireInternalToken(expected string) gin.HandlerFunc {
	expected = strings.TrimSpace(expected)
	return func(c *gin.Context) {
		if expected == "" {
			httpjson.Error(c, apierr.Internal("internal API token is not configured"))
			return
		}
		got := strings.TrimSpace(c.GetHeader("X-Internal-Token"))
		if got == "" || len(got) != len(expected) || subtle.ConstantTimeCompare([]byte(got), []byte(expected)) != 1 {
			httpjson.Error(c, apierr.Unauthenticated("invalid internal token"))
			return
		}
		c.Next()
	}
}

// DecodeLadderResponse reads a ladder response body.
//
// The gateway client uses this rather than declaring the shape again: the
// client once carried its own copy, it drifted into expecting a {"data":{...}}
// envelope this server has never sent, and every rung silently fell back to the
// static library. The package that writes the JSON owns the one decoder for it.
func DecodeLadderResponse(raw []byte) (LadderResponse, error) {
	var out LadderResponse
	if err := json.Unmarshal(raw, &out); err != nil {
		return LadderResponse{}, err
	}
	return out, nil
}
