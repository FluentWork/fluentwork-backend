package materials_test

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
	"github.com/FluentWork/fluentwork-backend/internal/materials"
)

func setupMaterials(t *testing.T) (*httpserver.Server, *materials.Service, string) {
	t.Helper()
	accountStore := account.NewMemoryStore()
	store := materials.NewMemoryStore()
	cfg := config.Config{
		HTTPAddr: ":0", AppEnv: "development", AuthJWTSecret: config.DevJWTSecret,
		AccessTokenTTL: 2 * time.Hour, RefreshTokenTTL: 24 * time.Hour,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	accountSvc := account.NewService(accountStore, account.NopReassigner{}, cfg, logger)
	accountHandler := account.NewHandler(accountSvc)
	svc := materials.NewService(store, nil, nil, logger)
	h := materials.NewHandler(svc, accountHandler)
	server := httpserver.New(cfg, logger, accountHandler, nil, nil, nil, nil, nil, nil, nil, h, accountStore.Ping)

	guestRec := httptest.NewRecorder()
	guestReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/guest", bytes.NewReader([]byte(`{"device_id":"mat-1"}`)))
	guestReq.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(guestRec, guestReq)
	var guest account.TokenResponse
	if err := json.Unmarshal(guestRec.Body.Bytes(), &guest); err != nil {
		t.Fatal(err)
	}
	return server, svc, guest.AccessToken
}

func TestPostMaterialHTTP_Accepted(t *testing.T) {
	server, _, token := setupMaterials(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/materials", bytes.NewReader([]byte(`{"kind":"paste","content":"I'll sync up."}`)))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusAccepted {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var body materials.CreateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil || body.MaterialID == "" || body.RefineStatus != materials.StatusQueued {
		t.Fatalf("body = %+v err=%v", body, err)
	}
}

func TestOpenAPIContainsMaterials(t *testing.T) {
	server, _, _ := setupMaterials(t)
	spec := httptest.NewRecorder()
	server.Handler().ServeHTTP(spec, httptest.NewRequest(http.MethodGet, "/openapi.yaml", nil))
	for _, needle := range [][]byte{
		[]byte("operationId: createMaterial"),
		[]byte("operationId: getMaterial"),
	} {
		if !bytes.Contains(spec.Body.Bytes(), needle) {
			t.Fatalf("openapi missing %s", needle)
		}
	}
}
