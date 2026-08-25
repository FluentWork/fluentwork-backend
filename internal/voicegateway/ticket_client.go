package voicegateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

// HTTPTicketConsumer calls app-server POST /internal/v1/tickets/consume.
type HTTPTicketConsumer struct {
	BaseURL    string
	Token      string
	HTTPClient *http.Client
}

type consumeRequest struct {
	Ticket string `json:"ticket"`
}

type consumeResponse struct {
	TicketID  string `json:"ticket_id"`
	SessionID string `json:"session_id"`
	UserID    string `json:"user_id"`
}

type errorBody struct {
	Code    string `json:"code"`
	Message string `json:"message"`
}

// ConsumeError is a structured failure from app-server ticket consume.
type ConsumeError struct {
	StatusCode int
	Code       string
	Message    string
}

func (e *ConsumeError) Error() string {
	if e == nil {
		return ""
	}
	if e.Code != "" {
		return fmt.Sprintf("%s: %s", e.Code, e.Message)
	}
	return e.Message
}

// Consume posts the raw ticket to app-server and returns the bound session.
func (c *HTTPTicketConsumer) Consume(ctx context.Context, rawTicket string) (ConsumedTicket, error) {
	base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if base == "" {
		return ConsumedTicket{}, fmt.Errorf("app-server base URL is required")
	}
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}

	payload, err := json.Marshal(consumeRequest{Ticket: rawTicket})
	if err != nil {
		return ConsumedTicket{}, err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+"/internal/v1/tickets/consume", bytes.NewReader(payload))
	if err != nil {
		return ConsumedTicket{}, err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Token", c.Token)

	resp, err := client.Do(req)
	if err != nil {
		return ConsumedTicket{}, fmt.Errorf("ticket consume request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()

	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return ConsumedTicket{}, err
	}
	if resp.StatusCode != http.StatusOK {
		var eb errorBody
		_ = json.Unmarshal(body, &eb)
		msg := strings.TrimSpace(eb.Message)
		if msg == "" {
			msg = strings.TrimSpace(string(body))
		}
		if msg == "" {
			msg = resp.Status
		}
		return ConsumedTicket{}, &ConsumeError{
			StatusCode: resp.StatusCode,
			Code:       eb.Code,
			Message:    msg,
		}
	}

	var out consumeResponse
	if err := json.Unmarshal(body, &out); err != nil {
		return ConsumedTicket{}, fmt.Errorf("decode consume response: %w", err)
	}
	if out.SessionID == "" || out.UserID == "" {
		return ConsumedTicket{}, fmt.Errorf("consume response missing session_id/user_id")
	}
	return ConsumedTicket(out), nil
}
