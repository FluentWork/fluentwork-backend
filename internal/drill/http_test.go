package drill_test

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
	"github.com/FluentWork/fluentwork-backend/internal/corpus"
	"github.com/FluentWork/fluentwork-backend/internal/drill"
	"github.com/FluentWork/fluentwork-backend/internal/httpserver"
)

func TestHandler_RoundAndJudgeHTTP(t *testing.T) {
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
	tokens, err := accountSvc.IssueGuest(context.Background(), "device-drill-1")
	if err != nil {
		t.Fatalf("guest: %v", err)
	}
	blocks := corpus.NewMemoryStore()
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	if _, err := blocks.SaveAcceptedBlocks(context.Background(), []corpus.PhraseBlock{{
		ID:             "block-1",
		UserID:         tokens.UserID,
		IntentZH:       "推动上线",
		ExpressionEN:   "Let's ship it",
		AnchorUserSaid: "ship it",
		SceneTag:       "review",
		FunctionTag:    "commit",
		State:          corpus.StateNew,
		NextDueAt:      now.Add(-time.Minute),
		EaseFactor:     2.5,
		CreatedAt:      now,
		UpdatedAt:      now,
	}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
	recs := drill.NewMemoryRecordStore()
	svc := drill.NewService(blocks, recs, &drill.LLMJudge{LLM: drill.StaticCompleter{Body: `{"pass":true}`}}, logger)
	drillHandler := drill.NewHandler(svc, accountHandler)
	server := httpserver.New(cfg, logger, accountHandler, nil, nil, nil, nil, nil, drillHandler, accountStore.Ping)

	roundRec := httptest.NewRecorder()
	roundReq := httptest.NewRequest(http.MethodGet, "/api/v1/drill/round", nil)
	roundReq.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	server.Handler().ServeHTTP(roundRec, roundReq)
	if roundRec.Code != http.StatusOK {
		t.Fatalf("round status=%d body=%s", roundRec.Code, roundRec.Body.String())
	}
	var round drill.Round
	if err := json.Unmarshal(roundRec.Body.Bytes(), &round); err != nil {
		t.Fatalf("decode round: %v", err)
	}
	if round.Size != 1 {
		t.Fatalf("round size=%d", round.Size)
	}

	judgeRec := httptest.NewRecorder()
	judgeReq := httptest.NewRequest(http.MethodPost, "/api/v1/drill/judge", bytes.NewReader([]byte(`{"block_id":"block-1","asr_text":"Let's ship it"}`)))
	judgeReq.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	judgeReq.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(judgeRec, judgeReq)
	if judgeRec.Code != http.StatusOK {
		t.Fatalf("judge status=%d body=%s", judgeRec.Code, judgeRec.Body.String())
	}

	other, err := accountSvc.IssueGuest(context.Background(), "device-drill-2")
	if err != nil {
		t.Fatalf("other guest: %v", err)
	}
	forbid := httptest.NewRecorder()
	forbidReq := httptest.NewRequest(http.MethodPost, "/api/v1/drill/judge", bytes.NewReader([]byte(`{"block_id":"block-1","asr_text":"hi"}`)))
	forbidReq.Header.Set("Authorization", "Bearer "+other.AccessToken)
	forbidReq.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(forbid, forbidReq)
	if forbid.Code != http.StatusForbidden {
		t.Fatalf("cross-user status=%d body=%s", forbid.Code, forbid.Body.String())
	}
}
