package account_test

import (
	"bytes"
	"context"
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
)

func setupServer(t *testing.T) (*httpserver.Server, *account.Service, *account.MemoryStore) {
	t.Helper()
	store := account.NewMemoryStore()
	cfg := config.Config{
		HTTPAddr:        ":0",
		AppEnv:          "development",
		AuthJWTSecret:   config.DevJWTSecret,
		AccessTokenTTL:  2 * time.Hour,
		RefreshTokenTTL: 24 * time.Hour,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	svc := account.NewService(store, account.NopReassigner{}, cfg, logger)
	server := httpserver.New(cfg, logger, account.NewHandler(svc), nil, nil, nil, store.Ping)
	return server, svc, store
}

func TestHealthz(t *testing.T) {
	server, _, _ := setupServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/healthz", nil)
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
}

func TestGuestAuthHTTPContract(t *testing.T) {
	server, _, _ := setupServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/guest", bytes.NewReader([]byte(`{"device_id":"device-1"}`)))
	req.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var body account.TokenResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.UserID == "" || !body.IsGuest || body.AccessToken == "" || body.TokenType != "Bearer" {
		t.Fatalf("unexpected body: %+v", body)
	}
}

func TestErrorEnvelopeIncludesRequestID(t *testing.T) {
	server, _, _ := setupServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/auth/guest", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Request-ID", "req-fixed-1")
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var body apierr.Body
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Code != "INVALID_ARGUMENT" || body.RequestID != "req-fixed-1" || body.Message == "" {
		t.Fatalf("unexpected error body: %+v", body)
	}
}

func TestMergeHTTPRequiresRegisteredTokenAndIsIdempotent(t *testing.T) {
	server, svc, store := setupServer(t)
	guestRec := httptest.NewRecorder()
	guestReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/guest", bytes.NewReader([]byte(`{"device_id":"device-1"}`)))
	guestReq.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(guestRec, guestReq)
	if guestRec.Code != http.StatusOK {
		t.Fatalf("guest status = %d body = %s", guestRec.Code, guestRec.Body.String())
	}

	unauth := httptest.NewRecorder()
	mergeReq := httptest.NewRequest(http.MethodPost, "/api/v1/account/merge", bytes.NewReader([]byte(`{"device_id":"device-1"}`)))
	mergeReq.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(unauth, mergeReq)
	if unauth.Code != http.StatusUnauthorized {
		t.Fatalf("expected 401, got %d body = %s", unauth.Code, unauth.Body.String())
	}

	var guestBody account.TokenResponse
	if err := json.Unmarshal(guestRec.Body.Bytes(), &guestBody); err != nil {
		t.Fatalf("decode guest: %v", err)
	}
	guestMerge := httptest.NewRecorder()
	guestMergeReq := httptest.NewRequest(http.MethodPost, "/api/v1/account/merge", bytes.NewReader([]byte(`{"device_id":"device-1"}`)))
	guestMergeReq.Header.Set("Content-Type", "application/json")
	guestMergeReq.Header.Set("Authorization", "Bearer "+guestBody.AccessToken)
	server.Handler().ServeHTTP(guestMerge, guestMergeReq)
	if guestMerge.Code != http.StatusForbidden {
		t.Fatalf("expected 403 for guest token, got %d body = %s", guestMerge.Code, guestMerge.Body.String())
	}

	now := time.Now().UTC()
	registered := account.User{ID: "registered-user-1", IsGuest: false, Status: account.UserStatusActive, CreatedAt: now, UpdatedAt: now}
	if err := store.CreateUser(context.Background(), registered); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	tokens, err := svc.IssueSession(context.Background(), registered)
	if err != nil {
		t.Fatalf("IssueSession: %v", err)
	}

	first := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/account/merge", bytes.NewReader([]byte(`{"device_id":"device-1"}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	server.Handler().ServeHTTP(first, req)
	if first.Code != http.StatusOK {
		t.Fatalf("merge status = %d body = %s", first.Code, first.Body.String())
	}
	var mergeBody account.MergeResponse
	if err := json.Unmarshal(first.Body.Bytes(), &mergeBody); err != nil {
		t.Fatalf("decode merge: %v", err)
	}
	if mergeBody.AlreadyMerged || mergeBody.UserID != registered.ID {
		t.Fatalf("unexpected merge body: %+v", mergeBody)
	}

	second := httptest.NewRecorder()
	req2 := httptest.NewRequest(http.MethodPost, "/api/v1/account/merge", bytes.NewReader([]byte(`{"device_id":"device-1"}`)))
	req2.Header.Set("Content-Type", "application/json")
	req2.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	server.Handler().ServeHTTP(second, req2)
	if second.Code != http.StatusOK {
		t.Fatalf("merge retry status = %d body = %s", second.Code, second.Body.String())
	}
	var retryBody account.MergeResponse
	if err := json.Unmarshal(second.Body.Bytes(), &retryBody); err != nil {
		t.Fatalf("decode retry: %v", err)
	}
	if !retryBody.AlreadyMerged {
		t.Fatalf("expected already_merged, got %+v", retryBody)
	}
}
