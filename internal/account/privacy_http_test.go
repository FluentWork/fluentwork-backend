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
	"github.com/FluentWork/fluentwork-backend/internal/aicost"
	"github.com/FluentWork/fluentwork-backend/internal/apierr"
	"github.com/FluentWork/fluentwork-backend/internal/config"
	"github.com/FluentWork/fluentwork-backend/internal/corpus"
	"github.com/FluentWork/fluentwork-backend/internal/drill"
	"github.com/FluentWork/fluentwork-backend/internal/httpserver"
	"github.com/FluentWork/fluentwork-backend/internal/session"
)

func setupPrivacyServer(t *testing.T) (*httpserver.Server, *account.Service, *account.PrivacyService, *corpus.MemoryStore, *account.MemoryStore, *account.TokenResponse) {
	t.Helper()
	accountStore := account.NewMemoryStore()
	cfg := config.Config{
		HTTPAddr:         ":0",
		AppEnv:           "development",
		AuthJWTSecret:    config.DevJWTSecret,
		AccessTokenTTL:   2 * time.Hour,
		RefreshTokenTTL:  24 * time.Hour,
		InternalAPIToken: config.DevInternalAPIToken,
	}
	logger := slog.New(slog.NewTextHandler(io.Discard, nil))
	accountSvc := account.NewService(accountStore, account.NopReassigner{}, cfg, logger)
	accountHandler := account.NewHandler(accountSvc)
	blocks := corpus.NewMemoryStore()
	sessions := session.NewMemoryStore()
	costs := aicost.NewMemoryStore()
	recs := drill.NewMemoryRecordStore()
	privacy := account.NewPrivacyService(accountStore, []account.DataWiper{
		corpus.PrivacyWiper{Store: blocks},
		session.PrivacyWiper{Store: sessions},
		aicost.PrivacyWiper{Store: costs},
	}, []account.HardDeleter{drill.RecordWiper{Store: recs}}, logger)
	accountHandler.SetPrivacy(privacy)
	drillHandler := drill.NewHandler(drill.NewService(blocks, recs, &drill.LLMJudge{LLM: drill.StaticCompleter{Body: `{"pass":true}`}}, logger), accountHandler)
	server := httpserver.New(cfg, logger, accountHandler, corpus.NewHandler(corpus.NewService(blocks, logger), accountHandler), nil, nil, nil, nil, drillHandler, accountStore.Ping)
	tokens, err := accountSvc.IssueGuest(context.Background(), "device-privacy-1")
	if err != nil {
		t.Fatalf("guest: %v", err)
	}
	return server, accountSvc, privacy, blocks, accountStore, &tokens
}

func TestPrivacyHTTP_WrongConfirmationAndIdempotentDelete(t *testing.T) {
	server, _, _, _, _, tokens := setupPrivacyServer(t)
	bad := httptest.NewRecorder()
	badReq := httptest.NewRequest(http.MethodDelete, "/api/v1/account/data", bytes.NewReader([]byte(`{"confirmation_code":"nope"}`)))
	badReq.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	badReq.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(bad, badReq)
	if bad.Code != http.StatusUnprocessableEntity {
		t.Fatalf("wrong confirm status=%d body=%s", bad.Code, bad.Body.String())
	}
	var envelope apierr.Body
	if err := json.Unmarshal(bad.Body.Bytes(), &envelope); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if envelope.Code != "FAILED_PRECONDITION" {
		t.Fatalf("code=%s", envelope.Code)
	}

	start := time.Now()
	ok := httptest.NewRecorder()
	okReq := httptest.NewRequest(http.MethodDelete, "/api/v1/account/data", bytes.NewReader([]byte(`{"confirmation_code":"DELETE-MY-DATA"}`)))
	okReq.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	okReq.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(ok, okReq)
	if ok.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", ok.Code, ok.Body.String())
	}
	if time.Since(start) > 15*time.Second {
		t.Fatalf("delete took %s", time.Since(start))
	}
	var first account.DeleteDataResult
	if err := json.Unmarshal(ok.Body.Bytes(), &first); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if first.AlreadyDeleted {
		t.Fatalf("first already deleted: %+v", first)
	}

	again := httptest.NewRecorder()
	againReq := httptest.NewRequest(http.MethodDelete, "/api/v1/account/data", bytes.NewReader([]byte(`{"confirmation_code":"DELETE-MY-DATA"}`)))
	againReq.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	againReq.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(again, againReq)
	if again.Code != http.StatusUnauthorized {
		t.Fatalf("deleted user should fail auth status=%d body=%s", again.Code, again.Body.String())
	}
}

func TestPrivacyHTTP_ExportAndUndelete(t *testing.T) {
	server, _, _, blocks, _, tokens := setupPrivacyServer(t)
	now := time.Now().UTC().Add(-time.Minute)
	if _, err := blocks.SaveAcceptedBlocks(context.Background(), []corpus.PhraseBlock{{
		ID:             "block-e2e",
		UserID:         tokens.UserID,
		IntentZH:       "推动上线",
		ExpressionEN:   "Let's ship it e2e",
		AnchorUserSaid: "ship e2e",
		SceneTag:       "review",
		FunctionTag:    "commit",
		State:          corpus.StateNew,
		NextDueAt:      now,
		EaseFactor:     2.5,
		CreatedAt:      now,
		UpdatedAt:      now,
	}}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	round := httptest.NewRecorder()
	roundReq := httptest.NewRequest(http.MethodGet, "/api/v1/drill/round", nil)
	roundReq.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	server.Handler().ServeHTTP(round, roundReq)
	if round.Code != http.StatusOK {
		t.Fatalf("round status=%d body=%s", round.Code, round.Body.String())
	}

	exp := httptest.NewRecorder()
	expReq := httptest.NewRequest(http.MethodPost, "/api/v1/account/export", bytes.NewReader([]byte(`{"email_to":"export@example.com"}`)))
	expReq.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	expReq.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(exp, expReq)
	if exp.Code != http.StatusOK {
		t.Fatalf("export status=%d body=%s", exp.Code, exp.Body.String())
	}
	var exported account.ExportDataResult
	if err := json.Unmarshal(exp.Body.Bytes(), &exported); err != nil {
		t.Fatalf("decode export: %v", err)
	}
	if exported.ExportID == "" || exported.EmailTo != "export@example.com" {
		t.Fatalf("export = %+v", exported)
	}

	del := httptest.NewRecorder()
	delReq := httptest.NewRequest(http.MethodDelete, "/api/v1/account/data", bytes.NewReader([]byte(`{"confirmation_code":"DELETE-MY-DATA"}`)))
	delReq.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	delReq.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(del, delReq)
	if del.Code != http.StatusOK {
		t.Fatalf("delete status=%d body=%s", del.Code, del.Body.String())
	}

	listed := httptest.NewRecorder()
	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/corpus/blocks", nil)
	listReq.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	server.Handler().ServeHTTP(listed, listReq)
	if listed.Code != http.StatusUnauthorized {
		t.Fatalf("post-delete list status=%d body=%s", listed.Code, listed.Body.String())
	}

	badTok := httptest.NewRecorder()
	badTokReq := httptest.NewRequest(http.MethodPost, "/internal/v1/support/undelete-user", bytes.NewReader([]byte(`{"user_id":"`+tokens.UserID+`","reason":"test"}`)))
	badTokReq.Header.Set("X-Internal-Token", "wrong-token-value!!!!")
	badTokReq.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(badTok, badTokReq)
	if badTok.Code != http.StatusUnauthorized {
		t.Fatalf("bad token status=%d body=%s", badTok.Code, badTok.Body.String())
	}

	und := httptest.NewRecorder()
	undReq := httptest.NewRequest(http.MethodPost, "/internal/v1/support/undelete-user", bytes.NewReader([]byte(`{"user_id":"`+tokens.UserID+`","reason":"test restore","actor":"qa"}`)))
	undReq.Header.Set("X-Internal-Token", config.DevInternalAPIToken)
	undReq.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(und, undReq)
	if und.Code != http.StatusOK {
		t.Fatalf("undelete status=%d body=%s", und.Code, und.Body.String())
	}

	listed2 := httptest.NewRecorder()
	listReq2 := httptest.NewRequest(http.MethodGet, "/api/v1/corpus/blocks", nil)
	listReq2.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	server.Handler().ServeHTTP(listed2, listReq2)
	if listed2.Code != http.StatusOK {
		t.Fatalf("post-undelete list status=%d body=%s", listed2.Code, listed2.Body.String())
	}
	if !bytes.Contains(listed2.Body.Bytes(), []byte("block-e2e")) && !bytes.Contains(listed2.Body.Bytes(), []byte("Let's ship it e2e")) {
		t.Fatalf("expected restored block in list: %s", listed2.Body.String())
	}
}
