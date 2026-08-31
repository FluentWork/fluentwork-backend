package httpserver

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/FluentWork/fluentwork-backend/internal/account"
	"github.com/FluentWork/fluentwork-backend/internal/config"
)

func TestReadyzOK(t *testing.T) {
	store := account.NewMemoryStore()
	cfg := config.Config{HTTPAddr: ":0", AppEnv: "development", AuthJWTSecret: config.DevJWTSecret}
	svc := account.NewService(store, account.NopReassigner{}, cfg, nil)
	server := New(cfg, nil, account.NewHandler(svc), nil, nil, nil, store.Ping)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/readyz", nil)
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
}

func TestUnknownRouteUsesErrorEnvelope(t *testing.T) {
	store := account.NewMemoryStore()
	cfg := config.Config{HTTPAddr: ":0", AppEnv: "development", AuthJWTSecret: config.DevJWTSecret}
	svc := account.NewService(store, account.NopReassigner{}, cfg, nil)
	server := New(cfg, nil, account.NewHandler(svc), nil, nil, nil, store.Ping)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/missing", nil)
	req.Header.Set("X-Request-ID", "missing-route")
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("X-Request-ID") != "missing-route" {
		t.Fatalf("request id header = %q", rec.Header().Get("X-Request-ID"))
	}
}
