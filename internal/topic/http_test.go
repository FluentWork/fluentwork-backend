package topic_test

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
	"github.com/FluentWork/fluentwork-backend/internal/config"
	"github.com/FluentWork/fluentwork-backend/internal/httpserver"
	"github.com/FluentWork/fluentwork-backend/internal/topic"
)

type stubLLM struct{ body string }

func (s stubLLM) Complete(context.Context, string) (string, error) { return s.body, nil }

func threeCardJSON() string {
	return `{"cards":[
		{"title":"Standup sync","prompt_en":"Share yesterday's progress.","prompt_zh":"同步昨天的进度","card_type":"warmup","seed_tags":["standup"]},
		{"title":"Clarify scope","prompt_en":"Ask what is in scope.","prompt_zh":"澄清范围","card_type":"practice","seed_tags":["review"]},
		{"title":"Propose next step","prompt_en":"Suggest one next action.","prompt_zh":"提出下一步","card_type":"stretch","seed_tags":["1on1"]}
	]}`
}

func setupTopic(t *testing.T) (*httpserver.Server, *topic.Service, string) {
	t.Helper()
	accountStore := account.NewMemoryStore()
	store := topic.NewMemoryStore()
	cfg := config.Config{
		HTTPAddr: ":0", AppEnv: "development", AuthJWTSecret: config.DevJWTSecret,
		AccessTokenTTL: 2 * time.Hour, RefreshTokenTTL: 24 * time.Hour,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	accountSvc := account.NewService(accountStore, account.NopReassigner{}, cfg, logger)
	accountHandler := account.NewHandler(accountSvc)
	gen := topic.NewGenerator(store, stubLLM{body: threeCardJSON()}, nil)
	svc := topic.NewService(store, gen, logger)
	h := topic.NewHandler(svc, accountHandler)
	server := httpserver.New(cfg, logger, accountHandler, nil, nil, nil, nil, nil, nil, nil, nil, h, accountStore.Ping)

	guestRec := httptest.NewRecorder()
	guestReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/guest", bytes.NewReader([]byte(`{"device_id":"topic-1"}`)))
	guestReq.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(guestRec, guestReq)
	var guest account.TokenResponse
	if err := json.Unmarshal(guestRec.Body.Bytes(), &guest); err != nil {
		t.Fatal(err)
	}
	return server, svc, guest.AccessToken
}

func TestCardsHTTP_ListAndCheckin(t *testing.T) {
	server, _, token := setupTopic(t)
	listRec := httptest.NewRecorder()
	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/topic-cards", nil)
	listReq.Header.Set("Authorization", "Bearer "+token)
	server.Handler().ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d body = %s", listRec.Code, listRec.Body.String())
	}
	var listed topic.ListResponse
	if err := json.Unmarshal(listRec.Body.Bytes(), &listed); err != nil || len(listed.Items) != 3 {
		t.Fatalf("list body = %s err=%v", listRec.Body.String(), err)
	}
	checkRec := httptest.NewRecorder()
	checkReq := httptest.NewRequest(http.MethodPost, "/api/v1/topic-cards/"+listed.Items[0].ID+"/checkin", bytes.NewReader([]byte(`{"reflection":"ok"}`)))
	checkReq.Header.Set("Authorization", "Bearer "+token)
	checkReq.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(checkRec, checkReq)
	if checkRec.Code != http.StatusOK {
		t.Fatalf("checkin status = %d body = %s", checkRec.Code, checkRec.Body.String())
	}
	dupRec := httptest.NewRecorder()
	dupReq := httptest.NewRequest(http.MethodPost, "/api/v1/topic-cards/"+listed.Items[0].ID+"/checkin", bytes.NewReader([]byte(`{}`)))
	dupReq.Header.Set("Authorization", "Bearer "+token)
	dupReq.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(dupRec, dupReq)
	if dupRec.Code != http.StatusConflict {
		t.Fatalf("dup status = %d body = %s", dupRec.Code, dupRec.Body.String())
	}
}

func TestOpenAPIContainsCards(t *testing.T) {
	server, _, _ := setupTopic(t)
	spec := httptest.NewRecorder()
	server.Handler().ServeHTTP(spec, httptest.NewRequest(http.MethodGet, "/openapi.yaml", nil))
	for _, needle := range [][]byte{
		[]byte("operationId: listTopicCards"),
		[]byte("operationId: checkinTopicCard"),
	} {
		if !bytes.Contains(spec.Body.Bytes(), needle) {
			t.Fatalf("openapi missing %s", needle)
		}
	}
}
