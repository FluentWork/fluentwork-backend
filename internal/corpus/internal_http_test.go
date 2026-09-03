package corpus_test

import (
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/gin-gonic/gin"

	"github.com/FluentWork/fluentwork-backend/internal/config"
	"github.com/FluentWork/fluentwork-backend/internal/corpus"
)

func TestInternalListBlocksAuthAndUserScoping(t *testing.T) {
	store := corpus.NewMemoryStore()
	if _, err := store.SaveAcceptedBlocks(context.Background(), []corpus.PhraseBlock{
		{
			ID:             "block-1",
			UserID:         "user-1",
			IntentZH:       "推动上线",
			ExpressionEN:   "Let's ship it.",
			AnchorUserSaid: "let's ship it",
			SceneTag:       "review",
			FunctionTag:    "commit",
		},
	}); err != nil {
		t.Fatalf("seed block: %v", err)
	}

	svc := corpus.NewService(store, nil)
	h := corpus.NewHandler(svc, nil)
	engine := gin.New()
	corpus.RegisterInternalRoutes(engine.Group("/internal/v1"), h, config.DevInternalAPIToken)

	unauth := httptest.NewRecorder()
	engine.ServeHTTP(unauth, httptest.NewRequest(http.MethodGet, "/internal/v1/corpus/blocks?user_id=user-1", nil))
	if unauth.Code != http.StatusUnauthorized {
		t.Fatalf("unauthorized status = %d body = %s", unauth.Code, unauth.Body.String())
	}

	missingUser := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/corpus/blocks", nil)
	req.Header.Set("X-Internal-Token", config.DevInternalAPIToken)
	engine.ServeHTTP(missingUser, req)
	if missingUser.Code != http.StatusBadRequest {
		t.Fatalf("missing user status = %d body = %s", missingUser.Code, missingUser.Body.String())
	}

	ok := httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodGet, "/internal/v1/corpus/blocks?user_id=user-1", nil)
	req.Header.Set("X-Internal-Token", config.DevInternalAPIToken)
	engine.ServeHTTP(ok, req)
	if ok.Code != http.StatusOK {
		t.Fatalf("ok status = %d body = %s", ok.Code, ok.Body.String())
	}
	var resp corpus.ListBlocksResponse
	if err := json.Unmarshal(ok.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode response: %v", err)
	}
	if len(resp.Items) != 1 || resp.Items[0].ExpressionEN != "Let's ship it." {
		t.Fatalf("unexpected items: %+v", resp.Items)
	}
}
