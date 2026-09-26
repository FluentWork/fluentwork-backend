package account_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/FluentWork/fluentwork-backend/internal/account"
	"github.com/FluentWork/fluentwork-backend/internal/httpserver"
)

func postJSON(t *testing.T, server *httpserver.Server, path string, body string) *httptest.ResponseRecorder {
	t.Helper()
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, path, bytes.NewReader([]byte(body)))
	req.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(rec, req)
	return rec
}

func issueGuest(t *testing.T, server *httpserver.Server, deviceID string) account.TokenResponse {
	t.Helper()
	rec := postJSON(t, server, "/api/v1/auth/guest", `{"device_id":"`+deviceID+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("guest status = %d body = %s", rec.Code, rec.Body.String())
	}
	var body account.TokenResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode guest: %v", err)
	}
	return body
}

func TestRefreshRouteExchangesTheTokenPair(t *testing.T) {
	server, _, _ := setupServer(t)
	guest := issueGuest(t, server, "device-refresh-1")

	rec := postJSON(t, server, "/api/v1/auth/refresh", `{"refresh_token":"`+guest.RefreshToken+`"}`)
	if rec.Code != http.StatusOK {
		t.Fatalf("refresh status = %d body = %s", rec.Code, rec.Body.String())
	}
	var body account.TokenResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode refresh: %v", err)
	}
	if body.AccessToken == "" || body.TokenType != "Bearer" {
		t.Fatalf("unexpected refresh body: %+v", body)
	}
	if body.UserID != guest.UserID {
		t.Fatalf("refresh changed the user: %s vs %s", body.UserID, guest.UserID)
	}
	if body.AccessToken == guest.AccessToken {
		t.Fatal("expected a freshly issued access token")
	}
}

func TestRefreshRouteRotatesTheRefreshToken(t *testing.T) {
	server, _, _ := setupServer(t)
	guest := issueGuest(t, server, "device-refresh-2")

	first := postJSON(t, server, "/api/v1/auth/refresh", `{"refresh_token":"`+guest.RefreshToken+`"}`)
	if first.Code != http.StatusOK {
		t.Fatalf("first refresh status = %d body = %s", first.Code, first.Body.String())
	}

	replay := postJSON(t, server, "/api/v1/auth/refresh", `{"refresh_token":"`+guest.RefreshToken+`"}`)
	if replay.Code != http.StatusUnauthorized {
		t.Fatalf("replayed refresh status = %d body = %s", replay.Code, replay.Body.String())
	}
}

func TestRefreshRouteRejectsAnEmptyBody(t *testing.T) {
	server, _, _ := setupServer(t)
	rec := postJSON(t, server, "/api/v1/auth/refresh", `{}`)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
}
