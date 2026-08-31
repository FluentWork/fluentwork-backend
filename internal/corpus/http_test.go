package corpus_test

import (
	"bytes"
	"context"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"net/url"
	"testing"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/account"
	"github.com/FluentWork/fluentwork-backend/internal/config"
	"github.com/FluentWork/fluentwork-backend/internal/corpus"
	"github.com/FluentWork/fluentwork-backend/internal/httpserver"
	"github.com/FluentWork/fluentwork-backend/internal/session"
)

func setupServer(t *testing.T) (*httpserver.Server, *account.Service, account.Store, *account.TokenResponse) {
	t.Helper()
	accountStore := account.NewMemoryStore()
	sessionStore := session.NewMemoryStore()
	corpusStore := corpus.NewMemoryStore()
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
	accountSvc := account.NewService(accountStore, account.ChainReassigner{
		session.Reassigner{Store: sessionStore},
		corpus.Reassigner{Store: corpusStore},
	}, cfg, logger)
	accountHandler := account.NewHandler(accountSvc)
	corpusSvc := corpus.NewService(corpusStore, logger)
	corpusHandler := corpus.NewHandler(corpusSvc, accountHandler)
	server := httpserver.New(cfg, logger, accountHandler, corpusHandler, nil, nil, accountStore.Ping)

	guestRec := httptest.NewRecorder()
	guestReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/guest", bytes.NewReader([]byte(`{"device_id":"device-corpus-1"}`)))
	guestReq.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(guestRec, guestReq)
	if guestRec.Code != http.StatusOK {
		t.Fatalf("guest status = %d body = %s", guestRec.Code, guestRec.Body.String())
	}
	var guestBody account.TokenResponse
	if err := json.Unmarshal(guestRec.Body.Bytes(), &guestBody); err != nil {
		t.Fatalf("decode guest: %v", err)
	}
	return server, accountSvc, accountStore, &guestBody
}

func TestBatchAcceptAndListCorpusBlocksHTTPContract(t *testing.T) {
	server, _, _, guest := setupServer(t)

	acceptRec := httptest.NewRecorder()
	acceptReq := httptest.NewRequest(http.MethodPost, "/api/v1/corpus/blocks/batch-accept", bytes.NewReader([]byte(`{
                "source_session_id":"session-1",
                "blocks":[
                        {"intent_zh":"说明下一步","expression_en":"I'll follow up tomorrow.","anchor_user_said":"I will follow up tomorrow.","scene_tag":"standup","function_tag":"commit"},
                        {"intent_zh":"说明阻塞","expression_en":"I'm blocked on the API review.","anchor_user_said":"I am blocked on the API review.","scene_tag":"standup","function_tag":"report"}
                ]
        }`)))
	acceptReq.Header.Set("Content-Type", "application/json")
	acceptReq.Header.Set("Authorization", "Bearer "+guest.AccessToken)
	server.Handler().ServeHTTP(acceptRec, acceptReq)
	if acceptRec.Code != http.StatusOK {
		t.Fatalf("batch accept status = %d body = %s", acceptRec.Code, acceptRec.Body.String())
	}
	var accepted corpus.BatchAcceptResponse
	if err := json.Unmarshal(acceptRec.Body.Bytes(), &accepted); err != nil {
		t.Fatalf("decode accepted: %v", err)
	}
	if accepted.AcceptedCount != 2 || len(accepted.Items) != 2 {
		t.Fatalf("unexpected batch accept response: %+v", accepted)
	}

	listRec := httptest.NewRecorder()
	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/corpus/blocks?scene=standup&limit=1", nil)
	listReq.Header.Set("Authorization", "Bearer "+guest.AccessToken)
	server.Handler().ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d body = %s", listRec.Code, listRec.Body.String())
	}
	var listed corpus.ListBlocksResponse
	if err := json.Unmarshal(listRec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode listed: %v", err)
	}
	if len(listed.Items) != 1 || listed.NextCursor == "" {
		t.Fatalf("unexpected list response: %+v", listed)
	}
}

func TestFavoriteUpdateAndDeleteCorpusBlockHTTPContract(t *testing.T) {
	server, _, _, guest := setupServer(t)

	acceptRec := httptest.NewRecorder()
	acceptReq := httptest.NewRequest(http.MethodPost, "/api/v1/corpus/blocks/batch-accept", bytes.NewReader([]byte(`{
                "source_session_id":"session-1",
                "blocks":[
                        {"intent_zh":"说明下一步","expression_en":"I'll follow up tomorrow.","anchor_user_said":"I will follow up tomorrow.","scene_tag":"standup","function_tag":"commit"}
                ]
        }`)))
	acceptReq.Header.Set("Content-Type", "application/json")
	acceptReq.Header.Set("Authorization", "Bearer "+guest.AccessToken)
	server.Handler().ServeHTTP(acceptRec, acceptReq)
	var accepted corpus.BatchAcceptResponse
	if err := json.Unmarshal(acceptRec.Body.Bytes(), &accepted); err != nil {
		t.Fatalf("decode accepted: %v", err)
	}
	blockID := accepted.Items[0].ID

	favRec := httptest.NewRecorder()
	favReq := httptest.NewRequest(http.MethodPost, "/api/v1/corpus/blocks/"+blockID+"/favorite", bytes.NewReader([]byte(`{"is_favorite":true,"pinned":true}`)))
	favReq.Header.Set("Content-Type", "application/json")
	favReq.Header.Set("Authorization", "Bearer "+guest.AccessToken)
	server.Handler().ServeHTTP(favRec, favReq)
	if favRec.Code != http.StatusOK {
		t.Fatalf("favorite status = %d body = %s", favRec.Code, favRec.Body.String())
	}
	var favorited corpus.PhraseBlockView
	if err := json.Unmarshal(favRec.Body.Bytes(), &favorited); err != nil {
		t.Fatalf("decode favorited: %v", err)
	}
	if !favorited.IsFavorite || favorited.PinnedAt == nil {
		t.Fatalf("unexpected favorite response: %+v", favorited)
	}

	putRec := httptest.NewRecorder()
	putReq := httptest.NewRequest(http.MethodPut, "/api/v1/corpus/blocks/"+blockID, bytes.NewReader([]byte(`{
                "intent_zh":"说明新的下一步",
                "expression_en":"I'll send the update this afternoon.",
                "anchor_user_said":"I will send the update later.",
                "scene_tag":"standup",
                "function_tag":"commit"
        }`)))
	putReq.Header.Set("Content-Type", "application/json")
	putReq.Header.Set("Authorization", "Bearer "+guest.AccessToken)
	server.Handler().ServeHTTP(putRec, putReq)
	if putRec.Code != http.StatusOK {
		t.Fatalf("put status = %d body = %s", putRec.Code, putRec.Body.String())
	}

	delRec := httptest.NewRecorder()
	delReq := httptest.NewRequest(http.MethodDelete, "/api/v1/corpus/blocks/"+blockID, nil)
	delReq.Header.Set("Authorization", "Bearer "+guest.AccessToken)
	server.Handler().ServeHTTP(delRec, delReq)
	if delRec.Code != http.StatusOK {
		t.Fatalf("delete status = %d body = %s", delRec.Code, delRec.Body.String())
	}
}

func TestGuestMergeMovesCorpusBlocksHTTPContract(t *testing.T) {
	server, accountSvc, accountStore, guest := setupServer(t)

	acceptRec := httptest.NewRecorder()
	acceptReq := httptest.NewRequest(http.MethodPost, "/api/v1/corpus/blocks/batch-accept", bytes.NewReader([]byte(`{
                "source_session_id":"session-merge-1",
                "blocks":[
                        {"intent_zh":"说明阻塞","expression_en":"I'm blocked on the API review.","anchor_user_said":"I am blocked on the API review.","scene_tag":"standup","function_tag":"report"}
                ]
        }`)))
	acceptReq.Header.Set("Content-Type", "application/json")
	acceptReq.Header.Set("Authorization", "Bearer "+guest.AccessToken)
	server.Handler().ServeHTTP(acceptRec, acceptReq)
	if acceptRec.Code != http.StatusOK {
		t.Fatalf("batch accept status = %d body = %s", acceptRec.Code, acceptRec.Body.String())
	}

	now := time.Now().UTC()
	registered := account.User{ID: "registered-corpus-1", IsGuest: false, Status: account.UserStatusActive, CreatedAt: now, UpdatedAt: now}
	if err := accountStore.CreateUser(context.Background(), registered); err != nil {
		t.Fatalf("CreateUser: %v", err)
	}
	tokens, err := accountSvc.IssueSession(context.Background(), registered)
	if err != nil {
		t.Fatalf("IssueSession: %v", err)
	}

	mergeRec := httptest.NewRecorder()
	mergeReq := httptest.NewRequest(http.MethodPost, "/api/v1/account/merge", bytes.NewReader([]byte(`{"device_id":"device-corpus-1"}`)))
	mergeReq.Header.Set("Content-Type", "application/json")
	mergeReq.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	server.Handler().ServeHTTP(mergeRec, mergeReq)
	if mergeRec.Code != http.StatusOK {
		t.Fatalf("merge status = %d body = %s", mergeRec.Code, mergeRec.Body.String())
	}

	listRec := httptest.NewRecorder()
	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/corpus/blocks?kw=blocked+on+the+API+review", nil)
	listReq.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	server.Handler().ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("list status = %d body = %s", listRec.Code, listRec.Body.String())
	}
	var listed corpus.ListBlocksResponse
	if err := json.Unmarshal(listRec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode listed: %v", err)
	}
	if len(listed.Items) != 1 {
		t.Fatalf("expected merged corpus block under registered user, got %+v", listed)
	}
}

func TestIncrementalCorpusBlocksHTTPContractIncludesDeletedTombstones(t *testing.T) {
	server, _, _, guest := setupServer(t)

	acceptRec := httptest.NewRecorder()
	acceptReq := httptest.NewRequest(http.MethodPost, "/api/v1/corpus/blocks/batch-accept", bytes.NewReader([]byte(`{
                "source_session_id":"session-delta-1",
                "blocks":[
                        {"intent_zh":"说明阻塞","expression_en":"I'm blocked on the API review.","anchor_user_said":"I am blocked on the API review.","scene_tag":"standup","function_tag":"report"}
                ]
        }`)))
	acceptReq.Header.Set("Content-Type", "application/json")
	acceptReq.Header.Set("Authorization", "Bearer "+guest.AccessToken)
	server.Handler().ServeHTTP(acceptRec, acceptReq)
	if acceptRec.Code != http.StatusOK {
		t.Fatalf("batch accept status = %d body = %s", acceptRec.Code, acceptRec.Body.String())
	}
	var accepted corpus.BatchAcceptResponse
	if err := json.Unmarshal(acceptRec.Body.Bytes(), &accepted); err != nil {
		t.Fatalf("decode accepted: %v", err)
	}
	blockID := accepted.Items[0].ID
	updatedAt := accepted.Items[0].UpdatedAt

	delRec := httptest.NewRecorder()
	delReq := httptest.NewRequest(http.MethodDelete, "/api/v1/corpus/blocks/"+blockID, nil)
	delReq.Header.Set("Authorization", "Bearer "+guest.AccessToken)
	server.Handler().ServeHTTP(delRec, delReq)
	if delRec.Code != http.StatusOK {
		t.Fatalf("delete status = %d body = %s", delRec.Code, delRec.Body.String())
	}

	listRec := httptest.NewRecorder()
	listReq := httptest.NewRequest(http.MethodGet, "/api/v1/corpus/blocks?updated_after="+url.QueryEscape(updatedAt.Add(-time.Second).Format(time.RFC3339Nano)), nil)
	listReq.Header.Set("Authorization", "Bearer "+guest.AccessToken)
	server.Handler().ServeHTTP(listRec, listReq)
	if listRec.Code != http.StatusOK {
		t.Fatalf("delta list status = %d body = %s", listRec.Code, listRec.Body.String())
	}
	var listed corpus.ListBlocksResponse
	if err := json.Unmarshal(listRec.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode delta listed: %v", err)
	}
	if len(listed.Items) != 1 || listed.Items[0].ID != blockID || listed.Items[0].DeletedAt == nil {
		t.Fatalf("expected deleted tombstone row, got %+v", listed)
	}
	if listed.CursorReset {
		t.Fatalf("expected cursor_reset=false, got %+v", listed)
	}
}
