package session

import (
	"crypto/subtle"
	"strings"

	"github.com/gin-gonic/gin"

	"github.com/FluentWork/fluentwork-backend/internal/apierr"
	"github.com/FluentWork/fluentwork-backend/internal/httpjson"
)

const internalTokenHeader = "X-Internal-Token"

// ConsumeTicketRequest is the body of POST /internal/v1/tickets/consume.
type ConsumeTicketRequest struct {
	Ticket string `json:"ticket"`
}

// ConsumeTicketResponse is returned after a one-time ticket is consumed.
type ConsumeTicketResponse struct {
	TicketID  string `json:"ticket_id"`
	SessionID string `json:"session_id"`
	UserID    string `json:"user_id"`
}

// RegisterInternalRoutes mounts gateway-facing internal routes under /internal/v1.
func RegisterInternalRoutes(rg gin.IRouter, h *Handler, expectedToken string) {
	rg.POST("/tickets/consume", requireInternalToken(expectedToken), h.PostConsumeTicket)
	rg.POST("/sessions/activate", requireInternalToken(expectedToken), h.PostActivate)
	rg.POST("/sessions/end", requireInternalToken(expectedToken), h.PostEnd)
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

// PostConsumeTicket handles POST /internal/v1/tickets/consume for voice-gateway.
func (h *Handler) PostConsumeTicket(c *gin.Context) {
	var req ConsumeTicketRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpjson.Error(c, apierr.InvalidArgument("invalid json body"))
		return
	}
	ticket, err := h.svc.ConsumeTicket(c.Request.Context(), req.Ticket)
	if err != nil {
		httpjson.Error(c, err)
		return
	}
	httpjson.OK(c, ConsumeTicketResponse{
		TicketID:  ticket.ID,
		SessionID: ticket.SessionID,
		UserID:    ticket.UserID,
	})
}

// PostActivate handles POST /internal/v1/sessions/activate for voice-gateway.
func (h *Handler) PostActivate(c *gin.Context) {
	var req ActivateRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpjson.Error(c, apierr.InvalidArgument("invalid json body"))
		return
	}
	result, err := h.svc.Activate(c.Request.Context(), req.SessionID)
	if err != nil {
		httpjson.Error(c, err)
		return
	}
	httpjson.OK(c, result)
}

// PostEnd handles POST /internal/v1/sessions/end for voice-gateway.
func (h *Handler) PostEnd(c *gin.Context) {
	var req EndRequest
	if err := c.ShouldBindJSON(&req); err != nil {
		httpjson.Error(c, apierr.InvalidArgument("invalid json body"))
		return
	}
	result, err := h.svc.End(c.Request.Context(), req)
	if err != nil {
		httpjson.Error(c, err)
		return
	}
	httpjson.OK(c, result)
}
