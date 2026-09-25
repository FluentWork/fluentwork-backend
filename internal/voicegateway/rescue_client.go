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

	"github.com/FluentWork/fluentwork-backend/internal/conversation"
	"github.com/FluentWork/fluentwork-backend/internal/voiceproto"
)

// HTTPRescueGenerator asks app-server for one rung of the rescue ladder.
//
// The gateway could hold provider credentials and call a model itself; it
// deliberately does not. Every other model call in this system is made by
// app-server, which owns the prompts, the routing and the cost ledger — a
// second spending path that writes no ai_cost_logs row would make the ladder the
// one feature whose cost nobody can see.
//
// Failures are ordinary here: the orchestrator falls back to its static ladder
// library, so this client returns an error rather than a substitute.
type HTTPRescueGenerator struct {
	BaseURL string
	Token   string
	Client  *http.Client
	Logger  *slog.Logger
}

// DefaultRescueGenTimeout bounds one generation when no budget is named. It is
// sized just inside the ladder spacing: the gateway gives generation
// DefaultRescueLevel1After (3s), so waiting longer only delays the fallback.
//
// Configurable through VOICE_RESCUE_GEN_TIMEOUT. Config.Validate refuses a value
// that does not fit inside the configured spacing, so this default is only the
// answer for an unset environment — it is not a promise about what runs.
const DefaultRescueGenTimeout = 2500 * time.Millisecond

// NewHTTPRescueGenerator constructs the client. A timeout of zero or less uses
// DefaultRescueGenTimeout.
func NewHTTPRescueGenerator(baseURL, token string, timeout time.Duration, logger *slog.Logger) *HTTPRescueGenerator {
	if logger == nil {
		logger = slog.Default()
	}
	if timeout <= 0 {
		timeout = DefaultRescueGenTimeout
	}
	return &HTTPRescueGenerator{
		BaseURL: strings.TrimRight(strings.TrimSpace(baseURL), "/"),
		Token:   strings.TrimSpace(token),
		Client:  &http.Client{Timeout: timeout},
		Logger:  logger.With("component", "voicegateway.rescue_client"),
	}
}

type rescueLadderBody struct {
	SessionID       string           `json:"session_id"`
	UserID          string           `json:"user_id"`
	TurnID          string           `json:"turn_id"`
	Level           int              `json:"level"`
	LastAIMessage   string           `json:"last_ai_message"`
	ScenarioContext string           `json:"scenario_context"`
	UserRole        string           `json:"user_role"`
	RecentTurns     []rescueTurnBody `json:"recent_turns,omitempty"`
}

type rescueTurnBody struct {
	Speaker string `json:"speaker"`
	Content string `json:"content"`
}

// GenerateRescue implements RescueGenerator.
//
// session/user/turn come from the context the caller holds, not from the
// conversation itself: they exist for logging and attribution, and the
// generator must not have to embed transport concerns in its state.
func (g *HTTPRescueGenerator) GenerateRescue(
	ctx context.Context,
	level conversation.RescueLevel,
	convCtx conversation.ConversationContext,
) (string, error) {
	if g == nil || g.BaseURL == "" {
		return "", fmt.Errorf("rescue client is not configured")
	}
	body := rescueLadderBody{
		SessionID:       convCtx.SessionID,
		UserID:          convCtx.UserID,
		TurnID:          convCtx.TurnID,
		Level:           int(level),
		LastAIMessage:   convCtx.LastAIMessage,
		ScenarioContext: convCtx.ScenarioContext,
		UserRole:        convCtx.UserRole,
	}
	for _, turn := range convCtx.RecentTurns {
		body.RecentTurns = append(body.RecentTurns, rescueTurnBody{Speaker: turn.Speaker, Content: turn.Content})
	}
	raw, err := json.Marshal(body)
	if err != nil {
		return "", err
	}
	req, err := http.NewRequestWithContext(ctx, http.MethodPost, g.BaseURL+"/internal/v1/rescue/ladder", bytes.NewReader(raw))
	if err != nil {
		return "", err
	}
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Token", g.Token)

	client := g.Client
	if client == nil {
		// A literally-constructed generator: give it the same budget the
		// constructor would have, rather than an unbounded wait.
		client = &http.Client{Timeout: DefaultRescueGenTimeout}
	}
	res, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = res.Body.Close() }()
	payload, err := io.ReadAll(io.LimitReader(res.Body, 64<<10))
	if err != nil {
		return "", err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return "", fmt.Errorf("rescue ladder http=%d body=%s", res.StatusCode, strings.TrimSpace(string(payload)))
	}
	// The server answers with the rung itself — {"level":2,"text":"..."} — with
	// no response envelope around it, so this decodes flat. It goes through the
	// server package's decoder rather than a struct declared here: the previous
	// copy of this shape in the client drifted into a {"data":{...}} wrapper
	// that app-server has never sent, and the unit test kept passing because it
	// built the same wrong body. One decoder means one place for the contract to
	// be wrong, and rescue_client_test.go drives it with the real handler.
	rung, err := conversation.DecodeLadderResponse(payload)
	if err != nil {
		return "", fmt.Errorf("decode rescue ladder: %w", err)
	}
	// A rung that is not the one we asked for is a mismatch, not a rescue: at
	// level 3 the model writes an English sentence and at 1 it hands back a
	// skeleton, so the wrong rung teaches the learner the wrong thing.
	if rung.Level != int(level) {
		return "", fmt.Errorf("rescue ladder level = %d, asked for %d", rung.Level, level)
	}
	text := strings.TrimSpace(rung.Text)
	if text == "" {
		return "", fmt.Errorf("rescue ladder came back empty")
	}
	if !voiceproto.ValidRescueLevel(int(level)) {
		return "", fmt.Errorf("invalid rescue level: %d", level)
	}
	return text, nil
}
