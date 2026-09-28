package session_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/FluentWork/fluentwork-backend/internal/account"
	"github.com/FluentWork/fluentwork-backend/internal/config"
	"github.com/FluentWork/fluentwork-backend/internal/configtest"
	"github.com/FluentWork/fluentwork-backend/internal/httpserver"
	"github.com/FluentWork/fluentwork-backend/internal/session"
)

func serverWithMiniTurnLimit(t *testing.T, miniTurnLimit int) *httpserver.Server {
	t.Helper()
	cfg := configtest.Config()
	cfg.MiniSessionTurnLimit = miniTurnLimit
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	accountStore := account.NewMemoryStore()
	sessionStore := session.NewMemoryStore()
	accountSvc := account.NewService(accountStore, session.Reassigner{Store: sessionStore}, cfg, logger)
	accountHandler := account.NewHandler(accountSvc)
	sessionSvc := session.NewService(sessionStore, cfg, logger)
	sessionHandler := session.NewHandler(sessionSvc, accountHandler)
	return httpserver.New(cfg, logger, accountHandler, nil, nil, sessionHandler, nil, nil, nil, nil, nil, nil, accountStore.Ping)
}

func sendToServer(t *testing.T, server *httpserver.Server, path, token, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	server.Handler().ServeHTTP(rec, req)
	return rec
}

func decodeBody(t *testing.T, rec *httptest.ResponseRecorder) map[string]any {
	t.Helper()
	var out map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &out); err != nil {
		t.Fatalf("decode %s: %v", rec.Body.String(), err)
	}
	return out
}

func guestAccessToken(t *testing.T, server *httpserver.Server, deviceID string) string {
	t.Helper()
	rec := sendToServer(t, server, "/api/v1/auth/guest", "", `{"device_id":"`+deviceID+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("guest status = %d body = %s", rec.Code, rec.Body.String())
	}
	token, _ := decodeBody(t, rec)["access_token"].(string)
	if token == "" {
		t.Fatalf("guest response has no access_token: %s", rec.Body.String())
	}
	return token
}

func createSessionWithLength(t *testing.T, server *httpserver.Server, deviceID, body string) *httptest.ResponseRecorder {
	t.Helper()
	token := guestAccessToken(t, server, deviceID)
	return sendToServer(t, server, "/api/v1/sessions", token, body)
}

func activateOverInternalAPI(t *testing.T, server *httpserver.Server, sessionID string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/sessions/activate",
		bytes.NewReader([]byte(`{"session_id":"`+sessionID+`"}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Token", config.DevInternalAPIToken)
	server.Handler().ServeHTTP(rec, req)
	return rec
}

func createdSessionID(t *testing.T, rec *httptest.ResponseRecorder) string {
	t.Helper()
	if rec.Code != http.StatusOK && rec.Code != http.StatusCreated {
		t.Fatalf("create session status = %d body = %s", rec.Code, rec.Body.String())
	}
	id, _ := decodeBody(t, rec)["session_id"].(string)
	if id == "" {
		t.Fatalf("create session response has no session_id: %s", rec.Body.String())
	}
	return id
}

func turnLimitOf(t *testing.T, rec *httptest.ResponseRecorder) float64 {
	t.Helper()
	if rec.Code != http.StatusOK {
		t.Fatalf("activate status = %d body = %s", rec.Code, rec.Body.String())
	}
	raw, ok := decodeBody(t, rec)["turn_limit"]
	if !ok {
		return 0
	}
	limit, _ := raw.(float64)
	return limit
}

// The turn cap is a number the operator sets, not a number this code knows. A
// hardcoded five would satisfy "mini sessions end" while nothing an operator
// changes has any effect — which is the failure the configurable-cap decision
// (D4) exists to avoid.
func TestActivate_MiniSessionTurnLimitComesFromConfig(t *testing.T) {
	t.Parallel()

	server := serverWithMiniTurnLimit(t, 3)
	created := createSessionWithLength(t, server, "device-mini-3", `{"scene_type":"demo","session_length":"mini"}`)
	sessionID := createdSessionID(t, created)

	if got := turnLimitOf(t, activateOverInternalAPI(t, server, sessionID)); got != 3 {
		t.Fatalf("turn_limit = %v, want 3 (MINI_SESSION_TURN_LIMIT)", got)
	}
}

func TestActivate_MiniTurnLimitFollowsASecondConfiguredValue(t *testing.T) {
	t.Parallel()

	server := serverWithMiniTurnLimit(t, 7)
	created := createSessionWithLength(t, server, "device-mini-7", `{"scene_type":"demo","session_length":"mini"}`)
	sessionID := createdSessionID(t, created)

	if got := turnLimitOf(t, activateOverInternalAPI(t, server, sessionID)); got != 7 {
		t.Fatalf("turn_limit = %v, want 7 (MINI_SESSION_TURN_LIMIT)", got)
	}
}

// An unset turn limit keeps the PRD default rather than meaning "no limit":
// zero is "unset" here for the same reason it is in the drill ladder, and
// normalizing it in one place is what stops a Config literal from quietly
// removing the cap.
func TestActivate_UnsetMiniTurnLimitFallsBackToTheDefault(t *testing.T) {
	t.Parallel()

	server := serverWithMiniTurnLimit(t, 0)
	created := createSessionWithLength(t, server, "device-mini-default", `{"scene_type":"demo","session_length":"mini"}`)
	sessionID := createdSessionID(t, created)

	if got := turnLimitOf(t, activateOverInternalAPI(t, server, sessionID)); got != 5 {
		t.Fatalf("turn_limit = %v, want the PRD default of 5", got)
	}
}

// The reverse direction: a standard session has no length contract, so the
// gateway must never be handed a cap it would then enforce.
func TestActivate_StandardSessionCarriesNoTurnLimit(t *testing.T) {
	t.Parallel()

	server := serverWithMiniTurnLimit(t, 5)

	for _, tc := range []struct {
		name     string
		deviceID string
		body     string
	}{
		{"named standard", "device-standard-named", `{"scene_type":"demo","session_length":"standard"}`},
		{"omitted", "device-standard-omitted", `{"scene_type":"demo"}`},
	} {
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			created := createSessionWithLength(t, server, tc.deviceID, tc.body)
			sessionID := createdSessionID(t, created)
			if got := turnLimitOf(t, activateOverInternalAPI(t, server, sessionID)); got != 0 {
				t.Fatalf("turn_limit = %v, want no limit for a standard session", got)
			}
		})
	}
}

func TestCreateSessionRejectsAnUnknownSessionLength(t *testing.T) {
	t.Parallel()

	server := serverWithMiniTurnLimit(t, 5)
	rec := createSessionWithLength(t, server, "device-bad-length", `{"scene_type":"demo","session_length":"epic"}`)

	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s, want 400", rec.Code, rec.Body.String())
	}
}
