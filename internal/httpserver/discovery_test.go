package httpserver

import (
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"slices"
	"sort"
	"strings"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/FluentWork/fluentwork-backend/internal/account"
	"github.com/FluentWork/fluentwork-backend/internal/config"
)

// discoveryEngine mounts the real account routes, one route no module
// registers, and one internal route. The unregistered route is what tells a
// list read from the router apart from a list somebody maintains by hand; the
// internal route is what tells the public surface apart from the whole one.
func discoveryEngine(t *testing.T) *gin.Engine {
	t.Helper()
	store := account.NewMemoryStore()
	cfg := config.Config{HTTPAddr: ":0", AppEnv: "development", AuthJWTSecret: config.DevJWTSecret}
	handler := account.NewHandler(account.NewService(store, account.NopReassigner{}, cfg, nil))

	engine := gin.New()
	engine.GET("/", discovery(engine))
	engine.GET("/healthz", liveness)
	apiGroup := engine.Group("/api/v1")
	account.RegisterRoutes(apiGroup, handler)
	apiGroup.GET("/probe/only-here", liveness)
	// Internal routes are nil-gated per module — account.RegisterInternalRoutes
	// returns early while the privacy service is absent — so mount one directly.
	// A test that mounted none would pass whatever the filter does.
	engine.Group("/internal/v1").POST("/probe/secret", liveness)
	return engine
}

// discoveryEndpoints performs GET / and returns the flat, sorted route list.
func discoveryEndpoints(t *testing.T, engine *gin.Engine) []string {
	t.Helper()
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, httptest.NewRequest(http.MethodGet, "/", nil))
	if rec.Code != http.StatusOK {
		t.Fatalf("GET / = %d body = %s", rec.Code, rec.Body.String())
	}
	var payload struct {
		Endpoints map[string][]string `json:"endpoints"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &payload); err != nil {
		t.Fatalf("decode discovery: %v (body = %s)", err, rec.Body.String())
	}
	flat := make([]string, 0, len(payload.Endpoints))
	for _, group := range payload.Endpoints {
		flat = append(flat, group...)
	}
	sort.Strings(flat)
	return flat
}

func TestDiscoveryListsEveryMountedRoute(t *testing.T) {
	got := discoveryEndpoints(t, discoveryEngine(t))
	want := []string{
		"GET /",
		"GET /api/v1/probe/only-here",
		"GET /healthz",
		"POST /api/v1/account/merge",
		"POST /api/v1/auth/guest",
		"POST /api/v1/auth/login",
		"POST /api/v1/auth/refresh",
		"POST /api/v1/auth/register",
	}
	if !slices.Equal(got, want) {
		t.Fatalf("discovery endpoints = %v, want %v", got, want)
	}
}

func TestDiscoveryWithholdsTheInternalSurface(t *testing.T) {
	engine := discoveryEngine(t)
	if !slices.ContainsFunc(engine.Routes(), func(route gin.RouteInfo) bool {
		return strings.HasPrefix(route.Path, "/internal/")
	}) {
		t.Fatal("the router carries no internal route, so withholding one proves nothing")
	}
	got := discoveryEndpoints(t, engine)
	if len(got) == 0 {
		t.Fatal("discovery listed no usable endpoints at all")
	}
	for _, entry := range got {
		if strings.Contains(entry, "/internal/") {
			t.Fatalf("discovery advertises the internal surface on an unauthenticated endpoint: %q", entry)
		}
	}
}
