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

// E2's appeal is a user-facing button, so the contract that matters is the HTTP
// one: judge returns the attempt id, the appeal takes it, and the block's
// schedule comes back to where it stood before the failed attempt.
func TestHandler_AppealHTTP(t *testing.T) {
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
	tokens, err := accountSvc.IssueGuest(context.Background(), "device-appeal-1")
	if err != nil {
		t.Fatalf("guest: %v", err)
	}

	blocks := corpus.NewMemoryStore()
	now := time.Now().UTC()
	due := now.Add(-time.Minute)
	if _, err := blocks.SaveAcceptedBlocks(context.Background(), []corpus.PhraseBlock{{
		ID:             "block-appeal",
		UserID:         tokens.UserID,
		IntentZH:       "推动上线",
		ExpressionEN:   "Let's ship it",
		AnchorUserSaid: "ship it",
		SceneTag:       "review",
		FunctionTag:    "commit",
		State:          corpus.StateTraining,
		SuccessStreak:  2,
		NextDueAt:      due,
		EaseFactor:     2.5,
		CreatedAt:      now.Add(-48 * time.Hour),
		UpdatedAt:      now.Add(-48 * time.Hour),
	}}); err != nil {
		t.Fatalf("seed: %v", err)
	}

	svc := drill.NewService(blocks, drill.NewMemoryRecordStore(),
		&drill.LLMJudge{LLM: drill.StaticCompleter{Body: `{"pass":false,"judge_reason":"not equivalent"}`}}, logger)
	server := httpserver.New(cfg, logger, accountHandler, nil, nil, nil, nil, nil,
		drill.NewHandler(svc, accountHandler), nil, nil, nil, accountStore.Ping)

	judgeRec := httptest.NewRecorder()
	judgeReq := httptest.NewRequest(http.MethodPost, "/api/v1/drill/judge",
		bytes.NewReader([]byte(`{"block_id":"block-appeal","asr_text":"we ship maybe"}`)))
	judgeReq.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	judgeReq.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(judgeRec, judgeReq)
	if judgeRec.Code != http.StatusOK {
		t.Fatalf("judge status=%d body=%s", judgeRec.Code, judgeRec.Body.String())
	}
	var judged drill.JudgeResponse
	if err := json.Unmarshal(judgeRec.Body.Bytes(), &judged); err != nil {
		t.Fatalf("decode judge: %v", err)
	}
	if judged.RecordID == 0 {
		t.Fatalf("judge response must carry record_id: %s", judgeRec.Body.String())
	}
	if judged.ASRText != "we ship maybe" {
		t.Fatalf("asr_text = %q, want the judged text", judged.ASRText)
	}

	appealBody, err := json.Marshal(drill.AppealRequest{RecordID: judged.RecordID})
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	appealRec := httptest.NewRecorder()
	appealReq := httptest.NewRequest(http.MethodPost, "/api/v1/drill/appeal", bytes.NewReader(appealBody))
	appealReq.Header.Set("Authorization", "Bearer "+tokens.AccessToken)
	appealReq.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(appealRec, appealReq)
	if appealRec.Code != http.StatusOK {
		t.Fatalf("appeal status=%d body=%s", appealRec.Code, appealRec.Body.String())
	}
	var appeal drill.AppealResponse
	if err := json.Unmarshal(appealRec.Body.Bytes(), &appeal); err != nil {
		t.Fatalf("decode appeal: %v", err)
	}
	if !appeal.Restored || appeal.SuccessStreak != 2 {
		t.Fatalf("appeal = %+v", appeal)
	}

	block, err := blocks.GetBlock(context.Background(), tokens.UserID, "block-appeal")
	if err != nil {
		t.Fatalf("GetBlock: %v", err)
	}
	if block.SuccessStreak != 2 || !block.NextDueAt.Equal(due) {
		t.Fatalf("schedule = streak %d due %v, want 2 / %v", block.SuccessStreak, block.NextDueAt, due)
	}

	// An unauthenticated appeal is rejected like every other drill route.
	anon := httptest.NewRecorder()
	anonReq := httptest.NewRequest(http.MethodPost, "/api/v1/drill/appeal", bytes.NewReader(appealBody))
	anonReq.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(anon, anonReq)
	if anon.Code != http.StatusUnauthorized {
		t.Fatalf("anonymous status=%d body=%s", anon.Code, anon.Body.String())
	}
}
