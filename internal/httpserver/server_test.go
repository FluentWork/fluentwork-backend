package httpserver

import (
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/FluentWork/fluentwork-backend/internal/account"
	"github.com/FluentWork/fluentwork-backend/internal/config"
)

func TestReadyzOK(t *testing.T) {
	store := account.NewMemoryStore()
	cfg := config.Config{HTTPAddr: ":0", AppEnv: "development", AuthJWTSecret: config.DevJWTSecret}
	svc := account.NewService(store, account.NopReassigner{}, cfg, nil)
	server := New(cfg, nil, account.NewHandler(svc), nil, nil, nil, nil, nil, nil, nil, store.Ping)
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
	server := New(cfg, nil, account.NewHandler(svc), nil, nil, nil, nil, nil, nil, nil, store.Ping)
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

func TestMetricsExposesTTSFallbackCounter(t *testing.T) {
	store := account.NewMemoryStore()
	cfg := config.Config{HTTPAddr: ":0", AppEnv: "development", AuthJWTSecret: config.DevJWTSecret}
	svc := account.NewService(store, account.NopReassigner{}, cfg, nil)
	server := New(cfg, nil, account.NewHandler(svc), nil, nil, nil, nil, nil, nil, nil, store.Ping)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/metrics", nil)
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	body := rec.Body.String()
	for _, name := range []string{
		"tts_fallback_triggered_total",
		"refine_parse_error_total",
		"privacy_delete_total",
		"review_eval_timeout_total",
		"review_eval_parse_error_total",
	} {
		if !strings.Contains(body, name) {
			t.Fatalf("metrics missing %s: %s", name, body)
		}
	}
}
