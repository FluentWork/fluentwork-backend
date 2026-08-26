package session_test

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
	"github.com/FluentWork/fluentwork-backend/internal/apierr"
	"github.com/FluentWork/fluentwork-backend/internal/config"
	"github.com/FluentWork/fluentwork-backend/internal/httpserver"
	"github.com/FluentWork/fluentwork-backend/internal/session"
)

func setupServer(t *testing.T) (*httpserver.Server, *account.Service) {
	t.Helper()
	accountStore := account.NewMemoryStore()
	sessionStore := session.NewMemoryStore()
	cfg := config.Config{
		HTTPAddr:           ":0",
		AppEnv:             "development",
		AuthJWTSecret:      config.DevJWTSecret,
		AccessTokenTTL:     2 * time.Hour,
		RefreshTokenTTL:    24 * time.Hour,
		VoiceGatewayWSSURL: "ws://127.0.0.1:8081/v1/voice",
		SessionTicketTTL:   60 * time.Second,
		InternalAPIToken:   config.DevInternalAPIToken,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	accountSvc := account.NewService(accountStore, session.Reassigner{Store: sessionStore}, cfg, logger)
	accountHandler := account.NewHandler(accountSvc)
	sessionSvc := session.NewService(sessionStore, cfg, logger)
	sessionHandler := session.NewHandler(sessionSvc, accountHandler)
	server := httpserver.New(cfg, logger, accountHandler, sessionHandler, accountStore.Ping)
	return server, accountSvc
}

func TestCreateSessionHTTPRequiresAuth(t *testing.T) {
	server, _ := setupServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var body apierr.Body
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Code != "UNAUTHENTICATED" {
		t.Fatalf("code = %q", body.Code)
	}
}

func TestCreateSessionHTTPContract(t *testing.T) {
	server, _ := setupServer(t)

	guestRec := httptest.NewRecorder()
	guestReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/guest", bytes.NewReader([]byte(`{"device_id":"device-session-1"}`)))
	guestReq.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(guestRec, guestReq)
	if guestRec.Code != http.StatusOK {
		t.Fatalf("guest status = %d body = %s", guestRec.Code, guestRec.Body.String())
	}
	var guestBody account.TokenResponse
	if err := json.Unmarshal(guestRec.Body.Bytes(), &guestBody); err != nil {
		t.Fatalf("decode guest: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions", bytes.NewReader([]byte(`{"scene_type":"demo"}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+guestBody.AccessToken)
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}

	var body session.CreateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.SessionID == "" || body.Ticket == "" || body.WSSURL == "" {
		t.Fatalf("unexpected body: %+v", body)
	}
	if body.TicketExpiresIn != 60 {
		t.Fatalf("ticket_expires_in = %d", body.TicketExpiresIn)
	}
	if body.Status != session.StatusCreated || body.SceneType != "demo" {
		t.Fatalf("unexpected metadata: %+v", body)
	}

	reviewRec := httptest.NewRecorder()
	reviewReq := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+body.SessionID+"/review", nil)
	reviewReq.Header.Set("Authorization", "Bearer "+guestBody.AccessToken)
	server.Handler().ServeHTTP(reviewRec, reviewReq)
	if reviewRec.Code != http.StatusOK {
		t.Fatalf("review status = %d body = %s", reviewRec.Code, reviewRec.Body.String())
	}
	var reviewBody session.ReviewPollResponse
	if err := json.Unmarshal(reviewRec.Body.Bytes(), &reviewBody); err != nil {
		t.Fatalf("decode review: %v", err)
	}
	if reviewBody.Status != session.ReviewPollPending || reviewBody.SessionID != body.SessionID {
		t.Fatalf("unexpected review body: %+v", reviewBody)
	}
}

func TestGetReviewHTTPRequiresAuth(t *testing.T) {
	server, _ := setupServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/s1/review", nil)
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
}

func TestCreateSessionHTTPAllowsEmptyBody(t *testing.T) {
	server, _ := setupServer(t)
	guestRec := httptest.NewRecorder()
	guestReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/guest", bytes.NewReader([]byte(`{"device_id":"device-session-2"}`)))
	guestReq.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(guestRec, guestReq)
	var guestBody account.TokenResponse
	if err := json.Unmarshal(guestRec.Body.Bytes(), &guestBody); err != nil {
		t.Fatalf("decode guest: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+guestBody.AccessToken)
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
}

func TestCreateSessionHTTPRejectsInvalidJSON(t *testing.T) {
	server, _ := setupServer(t)
	guestRec := httptest.NewRecorder()
	guestReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/guest", bytes.NewReader([]byte(`{"device_id":"device-session-3"}`)))
	guestReq.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(guestRec, guestReq)
	var guestBody account.TokenResponse
	if err := json.Unmarshal(guestRec.Body.Bytes(), &guestBody); err != nil {
		t.Fatalf("decode guest: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions", bytes.NewReader([]byte(`{`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+guestBody.AccessToken)
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
}

func TestOpenAPIDiscoveryEndpoints(t *testing.T) {
	server, _ := setupServer(t)

	root := httptest.NewRecorder()
	server.Handler().ServeHTTP(root, httptest.NewRequest(http.MethodGet, "/", nil))
	if root.Code != http.StatusOK {
		t.Fatalf("root status = %d body = %s", root.Code, root.Body.String())
	}
	var discovery map[string]any
	if err := json.Unmarshal(root.Body.Bytes(), &discovery); err != nil {
		t.Fatalf("decode discovery: %v", err)
	}
	if discovery["openapi"] != "/openapi.yaml" || discovery["api_prefix"] != "/api/v1" {
		t.Fatalf("unexpected discovery: %+v", discovery)
	}

	spec := httptest.NewRecorder()
	server.Handler().ServeHTTP(spec, httptest.NewRequest(http.MethodGet, "/openapi.yaml", nil))
	if spec.Code != http.StatusOK {
		t.Fatalf("openapi status = %d", spec.Code)
	}
	body := spec.Body.String()
	if !bytes.Contains(spec.Body.Bytes(), []byte("openapi: 3.0.3")) {
		t.Fatalf("openapi missing version header: %s", body[:min(80, len(body))])
	}
	if !bytes.Contains(spec.Body.Bytes(), []byte("/sessions")) {
		t.Fatal("openapi missing /sessions path")
	}
	if !bytes.Contains(spec.Body.Bytes(), []byte("/sessions/{id}/review")) {
		t.Fatal("openapi missing /sessions/{id}/review path")
	}
}
