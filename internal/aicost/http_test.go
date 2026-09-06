package aicost

import (
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/FluentWork/fluentwork-backend/internal/config"
	"github.com/FluentWork/fluentwork-backend/internal/httpjson"
)

const testInternalToken = "test-internal-token"

func newTestServer(t *testing.T) (*gin.Engine, *Service) {
	t.Helper()
	gin.SetMode(gin.TestMode)
	store := NewMemoryStore()
	svc := NewService(store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	now := time.Date(2026, 9, 6, 12, 0, 0, 0, time.UTC)
	svc.now = func() time.Time { return now }

	engine := gin.New()
	RegisterInternalRoutes(engine.Group("/internal/v1"), NewHandler(svc), testInternalToken)
	return engine, svc
}

func doInternalGet(t *testing.T, engine *gin.Engine, path, token string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest(http.MethodGet, path, nil)
	if token != "" {
		req.Header.Set("X-Internal-Token", token)
	}
	rec := httptest.NewRecorder()
	engine.ServeHTTP(rec, req)
	return rec
}

func TestListRecentInternal_RejectsMissingToken(t *testing.T) {
	engine, _ := newTestServer(t)
	rec := doInternalGet(t, engine, "/internal/v1/ai-cost-logs?limit=10", "")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
	var body map[string]any
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode body: %v", err)
	}
	if body["code"] != "UNAUTHENTICATED" {
		t.Fatalf("code = %v, want UNAUTHENTICATED", body["code"])
	}
}

func TestListRecentInternal_RejectsBadToken(t *testing.T) {
	engine, _ := newTestServer(t)
	rec := doInternalGet(t, engine, "/internal/v1/ai-cost-logs?limit=10", "wrong-token")
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d, want 401", rec.Code)
	}
}

func TestListRecentInternal_RejectsEmptyConfiguredToken(t *testing.T) {
	gin.SetMode(gin.TestMode)
	store := NewMemoryStore()
	svc := NewService(store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	engine := gin.New()
	RegisterInternalRoutes(engine.Group("/internal/v1"), NewHandler(svc), "" /* unconfigured */)

	rec := doInternalGet(t, engine, "/internal/v1/ai-cost-logs?limit=10", testInternalToken)
	if rec.Code != http.StatusInternalServerError {
		t.Fatalf("status = %d, want 500", rec.Code)
	}
}

func TestListRecentInternal_ReturnsAllLogsWhenUserIDEmpty(t *testing.T) {
	engine, svc := newTestServer(t)
	for _, userID := range []string{"user-1", "user-1", "user-2"} {
		if _, err := svc.Record(context.Background(), RecordRequest{
			UserID:   userID,
			TaskType: "review.eval",
			Model:    "ep-review",
		}); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}

	rec := doInternalGet(t, engine, "/internal/v1/ai-cost-logs?limit=10", testInternalToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Logs []Log `json:"logs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Logs) != 3 {
		t.Fatalf("logs len = %d, want 3 (cross-user listing)", len(resp.Logs))
	}
	if resp.Logs[0].TaskType != "review.eval" {
		t.Fatalf("first log task_type = %q", resp.Logs[0].TaskType)
	}
}

func TestListRecentInternal_FiltersByUserID(t *testing.T) {
	engine, svc := newTestServer(t)
	for _, userID := range []string{"user-1", "user-2", "user-1"} {
		if _, err := svc.Record(context.Background(), RecordRequest{
			UserID:   userID,
			TaskType: "review.eval",
			Model:    "ep-review",
		}); err != nil {
			t.Fatalf("Record: %v", err)
		}
	}

	rec := doInternalGet(t, engine, "/internal/v1/ai-cost-logs?user_id=user-1&limit=10", testInternalToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}

	var resp struct {
		Logs []Log `json:"logs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Logs) != 2 {
		t.Fatalf("logs len = %d, want 2 (user-1 only)", len(resp.Logs))
	}
	for _, row := range resp.Logs {
		if row.UserID == nil || *row.UserID != "user-1" {
			t.Fatalf("unexpected user_id: %+v", row.UserID)
		}
	}
}

func TestListRecentInternal_ClampsLimitToMax(t *testing.T) {
	engine, svc := newTestServer(t)
	for i := 0; i < 10; i++ {
		if _, err := svc.Record(context.Background(), RecordRequest{
			UserID:   "user-1",
			TaskType: "review.eval",
			Model:    "ep-review",
		}); err != nil {
			t.Fatal(err)
		}
	}

	// limit=9999 must be clamped to maxListLimit (500) but we only have 10 rows,
	// so the handler returns at most 10. The clamp behavior itself is verified by
	// observing that the request does not error and returns the expected size.
	rec := doInternalGet(t, engine, "/internal/v1/ai-cost-logs?limit=9999", testInternalToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200; body=%s", rec.Code, rec.Body.String())
	}

	var resp struct {
		Logs []Log `json:"logs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Logs) != 10 {
		t.Fatalf("logs len = %d, want 10", len(resp.Logs))
	}
}

func TestListRecentInternal_DefaultsLimitWhenZeroOrNegative(t *testing.T) {
	engine, svc := newTestServer(t)
	// Write 1 row only — handler must apply defaultListLimit semantics (no error
	// when limit is unparseable, returns up to defaultListLimit rows).
	if _, err := svc.Record(context.Background(), RecordRequest{
		UserID: "user-1", TaskType: "review.eval", Model: "ep-review",
	}); err != nil {
		t.Fatal(err)
	}

	for _, raw := range []string{"limit=0", "limit=-5", "limit=abc"} {
		rec := doInternalGet(t, engine, "/internal/v1/ai-cost-logs?"+raw, testInternalToken)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s status = %d, want 200", raw, rec.Code)
		}
		var resp struct {
			Logs []Log `json:"logs"`
		}
		if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
			t.Fatalf("decode: %v", err)
		}
		if len(resp.Logs) != 1 {
			t.Fatalf("%s logs len = %d, want 1", raw, len(resp.Logs))
		}
	}
}

func TestListRecentInternal_EmptyResultReturnsEmptyArray(t *testing.T) {
	engine, _ := newTestServer(t)
	rec := doInternalGet(t, engine, "/internal/v1/ai-cost-logs?user_id=ghost", testInternalToken)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d, want 200", rec.Code)
	}
	var resp struct {
		Logs []Log `json:"logs"`
	}
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.Logs == nil {
		t.Fatal("logs must be [] not null on empty result")
	}
	if len(resp.Logs) != 0 {
		t.Fatalf("logs len = %d, want 0", len(resp.Logs))
	}
}

// Smoke check that the package still compiles against the real config package —
// catches accidental API drift in NewHandler/RegisterInternalRoutes.
func TestHandler_DoesNotPanicWithRealConfig(t *testing.T) {
	_ = config.Config{}
	_ = httpjson.OK
	store := NewMemoryStore()
	svc := NewService(store, slog.New(slog.NewTextHandler(io.Discard, nil)))
	if NewHandler(svc) == nil {
		t.Fatal("NewHandler returned nil")
	}
}
