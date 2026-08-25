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

// SessionLifecycle notifies app-server of session.start / session.end.
type SessionLifecycle interface {
	Activate(ctx context.Context, sessionID string) error
	End(ctx context.Context, req EndSessionRequest) error
}

// EndSessionRequest is posted to app-server on WSS session.end.
type EndSessionRequest struct {
	SessionID   string
	DurationSec int
	Reason      string
	Utterances  []EndUtterance
}

// EndUtterance is one transcript turn for B4 persistence.
type EndUtterance struct {
	Seq     int
	Speaker string
	Text    string
}

// HTTPSessionClient calls app-server internal session lifecycle endpoints.
type HTTPSessionClient struct {
	BaseURL    string
	Token      string
	HTTPClient *http.Client
}

type activateBody struct {
	SessionID string `json:"session_id"`
}

type endBody struct {
	SessionID   string             `json:"session_id"`
	DurationSec int                `json:"duration_sec"`
	Reason      string             `json:"reason"`
	Utterances  []endUtteranceBody `json:"utterances"`
}

type endUtteranceBody struct {
	Seq     int    `json:"seq"`
	Speaker string `json:"speaker"`
	Text    string `json:"text"`
}

// Activate marks the practice session active.
func (c *HTTPSessionClient) Activate(ctx context.Context, sessionID string) error {
	return c.post(ctx, "/internal/v1/sessions/activate", activateBody{SessionID: sessionID})
}

// End persists session end + utterances.
func (c *HTTPSessionClient) End(ctx context.Context, req EndSessionRequest) error {
	body := endBody{
		SessionID:   req.SessionID,
		DurationSec: req.DurationSec,
		Reason:      req.Reason,
		Utterances:  make([]endUtteranceBody, 0, len(req.Utterances)),
	}
	for _, u := range req.Utterances {
		body.Utterances = append(body.Utterances, endUtteranceBody(u))
	}
	return c.post(ctx, "/internal/v1/sessions/end", body)
}

func (c *HTTPSessionClient) post(ctx context.Context, path string, payload any) error {
	base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if base == "" {
		return fmt.Errorf("app-server base URL is required")
	}
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		return err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, bytes.NewReader(raw))
	if err != nil {
		return err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Token", c.Token)

	resp, err := client.Do(req)
	if err != nil {
		return fmt.Errorf("session lifecycle request: %w", err)
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		return err
	}
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		var eb errorBody
		_ = json.Unmarshal(body, &eb)
		msg := strings.TrimSpace(eb.Message)
		if msg == "" {
			msg = strings.TrimSpace(string(body))
		}
		if msg == "" {
			msg = resp.Status
		}
		return fmt.Errorf("%s", msg)
	}
	return nil
}
