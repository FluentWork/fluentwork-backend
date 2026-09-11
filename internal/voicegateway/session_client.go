package voicegateway

import (
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"time"

	"github.com/FluentWork/fluentwork-backend/pkg/logx"
)

// SessionLifecycle notifies app-server of session.start / session.end, and
// resolves the one thing the gateway needs to *read* back.
type SessionLifecycle interface {
	Activate(ctx context.Context, sessionID string) error
	End(ctx context.Context, req EndSessionRequest) error
	// ContinuationContext returns the tail of an earlier session's transcript,
	// or an empty slice when there is nothing to continue from.
	//
	// Both ids go over the wire because the ownership check cannot happen
	// here: the current session's owner is known to app-server (from the
	// ticket it issued), the previous session's owner only to the store, and
	// `previousSessionID` comes from a frame the client wrote.
	//
	// A refusal is an error, but callers are expected to **carry on without
	// context** rather than fail the session — see handler's session.start.
	ContinuationContext(ctx context.Context, currentSessionID, previousSessionID string, limit int) ([]ContinuationTurn, error)
}

// EndSessionRequest is posted to app-server on WSS session.end.
type EndSessionRequest struct {
	SessionID   string
	DurationSec int
	Reason      string
	Utterances  []EndUtterance
	// VoiceUsage is the audio this session moved. Nil when the provider cannot
	// report it (mock, dev-echo); the wire field is omitted then, and the
	// app-server writes no cost row rather than a zero-valued one.
	VoiceUsage *VoiceUsage
}

// EndUtterance is one transcript turn for B4 persistence.
type EndUtterance struct {
	Seq     int
	Speaker string
	Text    string
	// Interrupted marks a reply the user cut off, in which case Text is only
	// the part that had been delivered when they did — the transcript records
	// what was heard, not what was generated. See `77_` P1-14.
	Interrupted bool `json:"interrupted,omitempty"`
}

// HTTPSessionClient calls app-server internal session lifecycle endpoints.
type HTTPSessionClient struct {
	BaseURL    string
	Token      string
	HTTPClient *http.Client
	Logger     *slog.Logger
}

type activateBody struct {
	SessionID string `json:"session_id"`
}

type endBody struct {
	SessionID   string             `json:"session_id"`
	DurationSec int                `json:"duration_sec"`
	Reason      string             `json:"reason"`
	Utterances  []endUtteranceBody `json:"utterances"`
	VoiceUsage  *endVoiceUsageBody `json:"voice_usage,omitempty"`
}

// endVoiceUsageBody is the wire shape for VoiceUsage. A separate type from
// VoiceUsage for the same reason endUtteranceBody is separate from
// EndUtterance: the JSON contract is not the in-process type, and keeping them
// apart means a rename in one cannot silently change the wire.
type endVoiceUsageBody struct {
	UplinkMS   int64  `json:"uplink_ms"`
	DownlinkMS int64  `json:"downlink_ms"`
	Model      string `json:"model,omitempty"`
}

type endUtteranceBody struct {
	Seq     int    `json:"seq"`
	Speaker string `json:"speaker"`
	Text    string `json:"text"`
	// Interrupted travels to the app-server so the persisted transcript can say
	// "the user cut this off" rather than presenting a partial reply as whole.
	Interrupted bool `json:"interrupted,omitempty"`
}

type continuationContextBody struct {
	CurrentSessionID  string `json:"current_session_id"`
	PreviousSessionID string `json:"previous_session_id"`
	Limit             int    `json:"limit"`
}

type continuationContextResponse struct {
	Utterances []continuationUtteranceBody `json:"utterances"`
}

type continuationUtteranceBody struct {
	Seq     int    `json:"seq"`
	Speaker string `json:"speaker"`
	Text    string `json:"text"`
}

// ContinuationContext reads the tail of a previous session's transcript.
func (c *HTTPSessionClient) ContinuationContext(
	ctx context.Context,
	currentSessionID string,
	previousSessionID string,
	limit int,
) ([]ContinuationTurn, error) {
	var resp continuationContextResponse
	err := c.postInto(ctx, "/internal/v1/sessions/continuation-context", continuationContextBody{
		CurrentSessionID:  currentSessionID,
		PreviousSessionID: previousSessionID,
		Limit:             limit,
	}, &resp)
	if err != nil {
		return nil, err
	}
	// A conversion rather than a field-by-field literal. The two types are
	// deliberately separate — the wire shape is not the in-process type — but
	// while they happen to match, the conversion is the better of the two: if
	// either gains a field, this stops compiling instead of silently dropping
	// it. staticcheck S1016 asks for exactly this.
	out := make([]ContinuationTurn, 0, len(resp.Utterances))
	for _, u := range resp.Utterances {
		out = append(out, ContinuationTurn(u))
	}
	return out, nil
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
	if usage := req.VoiceUsage; usage != nil {
		body.VoiceUsage = &endVoiceUsageBody{
			UplinkMS:   usage.UplinkMS,
			DownlinkMS: usage.DownlinkMS,
			Model:      usage.Model,
		}
	}
	return c.post(ctx, "/internal/v1/sessions/end", body)
}

func (c *HTTPSessionClient) post(ctx context.Context, path string, payload any) error {
	return c.postInto(ctx, path, payload, nil)
}

// postInto is post plus a response target. The body is already read for the
// error path, so decoding it on success costs a call rather than a round trip —
// which is why continuation context can be a plain request instead of the
// gateway holding its own client and token.
func (c *HTTPSessionClient) postInto(ctx context.Context, path string, payload any, out any) error {
	base := strings.TrimRight(strings.TrimSpace(c.BaseURL), "/")
	if base == "" {
		return fmt.Errorf("app-server base URL is required")
	}
	seg := logx.Begin(c.Logger, "voice.session_lifecycle",
		"component", "voicegateway.session_client",
		"path", path,
		"base_url", base,
	)
	var reqErr error
	var endAttrs []any
	defer func() {
		seg.End(reqErr, endAttrs...)
	}()
	client := c.HTTPClient
	if client == nil {
		client = &http.Client{Timeout: 5 * time.Second}
	}
	raw, err := json.Marshal(payload)
	if err != nil {
		reqErr = err
		return reqErr
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, base+path, bytes.NewReader(raw))
	if err != nil {
		reqErr = err
		return reqErr
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Token", c.Token)

	resp, err := client.Do(req)
	if err != nil {
		reqErr = fmt.Errorf("session lifecycle request: %w", err)
		return reqErr
	}
	defer func() { _ = resp.Body.Close() }()
	body, err := io.ReadAll(io.LimitReader(resp.Body, 1<<20))
	if err != nil {
		reqErr = err
		return reqErr
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
		reqErr = fmt.Errorf("%s", msg)
		return reqErr
	}
	if out != nil {
		if err := json.Unmarshal(body, out); err != nil {
			reqErr = fmt.Errorf("decode %s response: %w", path, err)
			return reqErr
		}
	}
	endAttrs = []any{
		"status", resp.StatusCode,
	}
	return nil
}
