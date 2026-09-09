package sessionhistory_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/account"
	"github.com/FluentWork/fluentwork-backend/internal/config"
	"github.com/FluentWork/fluentwork-backend/internal/httpserver"
	"github.com/FluentWork/fluentwork-backend/internal/session"
	"github.com/FluentWork/fluentwork-backend/internal/sessionhistory"
)

func setupHistory(t *testing.T) (*httpserver.Server, *session.MemoryStore, string) {
	t.Helper()
	accountStore := account.NewMemoryStore()
	sessionStore := session.NewMemoryStore()
	cfg := config.Config{
		HTTPAddr:        ":0",
		AppEnv:          "development",
		AuthJWTSecret:   config.DevJWTSecret,
		AccessTokenTTL:  2 * time.Hour,
		RefreshTokenTTL: 24 * time.Hour,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	accountSvc := account.NewService(accountStore, session.Reassigner{Store: sessionStore}, cfg, logger)
	accountHandler := account.NewHandler(accountSvc)
	history := sessionhistory.NewHandler(sessionhistory.NewService(sessionStore, nil, logger), accountHandler)
	server := httpserver.New(cfg, logger, accountHandler, nil, nil, nil, nil, nil, nil, history, accountStore.Ping)

	guestRec := httptest.NewRecorder()
	guestReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/guest", bytes.NewReader([]byte(`{"device_id":"hist-1"}`)))
	guestReq.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(guestRec, guestReq)
	if guestRec.Code != http.StatusOK {
		t.Fatalf("guest status = %d body = %s", guestRec.Code, guestRec.Body.String())
	}
	var guest account.TokenResponse
	if err := json.Unmarshal(guestRec.Body.Bytes(), &guest); err != nil {
		t.Fatalf("decode guest: %v", err)
	}
	return server, sessionStore, guest.AccessToken
}

func TestListSessionsHTTP_Empty(t *testing.T) {
	server, _, token := setupHistory(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var page sessionhistory.SessionListPage
	if err := json.Unmarshal(rec.Body.Bytes(), &page); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(page.Items) != 0 {
		t.Fatalf("items = %+v", page.Items)
	}
}

func TestGetSessionHTTP_CrossUserForbidden(t *testing.T) {
	server, store, token := setupHistory(t)
	if err := store.CreateSession(t.Context(), session.Session{
		ID: "other-s", UserID: "someone-else", SceneType: "demo",
		Status: session.StatusCreated, CreatedAt: time.Now().UTC(), UpdatedAt: time.Now().UTC(),
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/other-s", nil)
	req.Header.Set("Authorization", "Bearer "+token)
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
}

func TestOpenAPIContainsSessionHistory(t *testing.T) {
	server, _, _ := setupHistory(t)
	spec := httptest.NewRecorder()
	server.Handler().ServeHTTP(spec, httptest.NewRequest(http.MethodGet, "/openapi.yaml", nil))
	if spec.Code != http.StatusOK {
		t.Fatalf("status = %d", spec.Code)
	}
	body := spec.Body.Bytes()
	for _, needle := range [][]byte{
		[]byte("operationId: listSessions"),
		[]byte("operationId: getSession"),
		[]byte("SessionListPage"),
		[]byte("SessionDetail"),
	} {
		if !bytes.Contains(body, needle) {
			t.Fatalf("openapi missing %s", needle)
		}
	}
}
