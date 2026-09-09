package corpus_test

import (
	"bytes"
	"context"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"sync"
	"testing"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/FluentWork/fluentwork-backend/internal/config"
	"github.com/FluentWork/fluentwork-backend/internal/corpus"
)

func newHitsEngine(t *testing.T, store *corpus.MemoryStore, token string) *gin.Engine {
	t.Helper()
	gin.SetMode(gin.TestMode)
	svc := corpus.NewService(store, nil)
	h := corpus.NewHandler(svc, nil)
	engine := gin.New()
	corpus.RegisterInternalRoutes(engine.Group("/internal/v1"), h, token)
	return engine
}

func seedHitsBlock(t *testing.T, store *corpus.MemoryStore) {
	t.Helper()
	now := time.Date(2026, 9, 9, 12, 0, 0, 0, time.UTC)
	if _, err := store.SaveAcceptedBlocks(context.Background(), []corpus.PhraseBlock{{
		ID:             "block-1",
		UserID:         "user-1",
		IntentZH:       "推动上线",
		ExpressionEN:   "Let's ship it.",
		AnchorUserSaid: "let's ship it",
		SceneTag:       "review",
		FunctionTag:    "commit",
		State:          corpus.StateNew,
		NextDueAt:      now,
		CreatedAt:      now,
		UpdatedAt:      now,
	}}); err != nil {
		t.Fatalf("seed: %v", err)
	}
}

func TestRequireInternalToken_Missing(t *testing.T) {
	engine := newHitsEngine(t, corpus.NewMemoryStore(), config.DevInternalAPIToken)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/voicegateway/hits", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Content-Type", "application/json")
	engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
}

func TestRequireInternalToken_Invalid(t *testing.T) {
	engine := newHitsEngine(t, corpus.NewMemoryStore(), config.DevInternalAPIToken)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/internal/v1/sessions/session-1/recent-hits", nil)
	req.Header.Set("X-Internal-Token", "wrong-token")
	engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
}

func TestRequireInternalToken_Valid(t *testing.T) {
	store := corpus.NewMemoryStore()
	seedHitsBlock(t, store)
	engine := newHitsEngine(t, store, config.DevInternalAPIToken)
	body := []byte(`{"user_id":"user-1","session_id":"session-1","turn_id":"turn-1","hits":[{"block_id":"block-1","detected_at_ms":1000}]}`)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/voicegateway/hits", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Token", config.DevInternalAPIToken)
	engine.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var resp corpus.RecordHitsResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if resp.RecordedCount != 1 {
		t.Fatalf("recorded_count = %d", resp.RecordedCount)
	}
}

func TestHitsHTTP_RecentHitsContract(t *testing.T) {
	store := corpus.NewMemoryStore()
	seedHitsBlock(t, store)
	engine := newHitsEngine(t, store, config.DevInternalAPIToken)

	post := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/internal/v1/voicegateway/hits", bytes.NewReader([]byte(
		`{"user_id":"user-1","session_id":"session-1","turn_id":"turn-1","hits":[{"block_id":"block-1","detected_at_ms":5000}]}`,
	)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("X-Internal-Token", config.DevInternalAPIToken)
	engine.ServeHTTP(post, req)
	if post.Code != http.StatusOK {
		t.Fatalf("post status = %d body = %s", post.Code, post.Body.String())
	}

	get := httptest.NewRecorder()
	greq := httptest.NewRequest(http.MethodGet, "/internal/v1/sessions/session-1/recent-hits?lookback_turns=8", nil)
	greq.Header.Set("X-Internal-Token", config.DevInternalAPIToken)
	engine.ServeHTTP(get, greq)
	if get.Code != http.StatusOK {
		t.Fatalf("get status = %d body = %s", get.Code, get.Body.String())
	}
	var resp corpus.RecentHitsResult
	if err := json.Unmarshal(get.Body.Bytes(), &resp); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(resp.Hits) != 1 || resp.Hits[0].ChunkEN != "Let's ship it." {
		t.Fatalf("hits = %+v", resp.Hits)
	}
	if resp.TTLAtMs != 5000+corpus.RecentHitTTLMs {
		t.Fatalf("ttl_at_ms = %d", resp.TTLAtMs)
	}
}

func TestHitsHTTP_LookbackTurnsBoundary(t *testing.T) {
	store := corpus.NewMemoryStore()
	seedHitsBlock(t, store)
	engine := newHitsEngine(t, store, config.DevInternalAPIToken)
	post := httptest.NewRequest(http.MethodPost, "/internal/v1/voicegateway/hits", bytes.NewReader([]byte(
		`{"user_id":"user-1","session_id":"session-1","turn_id":"turn-1","hits":[{"block_id":"block-1","detected_at_ms":1000}]}`,
	)))
	post.Header.Set("Content-Type", "application/json")
	post.Header.Set("X-Internal-Token", config.DevInternalAPIToken)
	engine.ServeHTTP(httptest.NewRecorder(), post)

	zero := httptest.NewRecorder()
	zreq := httptest.NewRequest(http.MethodGet, "/internal/v1/sessions/session-1/recent-hits?lookback_turns=0", nil)
	zreq.Header.Set("X-Internal-Token", config.DevInternalAPIToken)
	engine.ServeHTTP(zero, zreq)
	var zresp corpus.RecentHitsResult
	if err := json.Unmarshal(zero.Body.Bytes(), &zresp); err != nil {
		t.Fatalf("decode 0: %v", err)
	}
	if len(zresp.Hits) != 0 || zresp.TTLAtMs != 0 {
		t.Fatalf("lookback 0 = %+v", zresp)
	}

	one := httptest.NewRecorder()
	oreq := httptest.NewRequest(http.MethodGet, "/internal/v1/sessions/session-1/recent-hits?lookback_turns=1", nil)
	oreq.Header.Set("X-Internal-Token", config.DevInternalAPIToken)
	engine.ServeHTTP(one, oreq)
	var oresp corpus.RecentHitsResult
	if err := json.Unmarshal(one.Body.Bytes(), &oresp); err != nil {
		t.Fatalf("decode 1: %v", err)
	}
	if len(oresp.Hits) != 1 {
		t.Fatalf("lookback 1 = %+v", oresp)
	}
}

func TestHitsHTTP_ConcurrentPostP99(t *testing.T) {
	store := corpus.NewMemoryStore()
	seedHitsBlock(t, store)
	engine := newHitsEngine(t, store, config.DevInternalAPIToken)
	body := []byte(`{"user_id":"user-1","session_id":"session-1","turn_id":"turn-1","hits":[{"block_id":"block-1","detected_at_ms":1000}]}`)

	const n = 100
	lat := make([]time.Duration, n)
	errs := make(chan string, n)
	var wg sync.WaitGroup
	wg.Add(n)
	start := time.Now()
	for i := 0; i < n; i++ {
		go func(i int) {
			defer wg.Done()
			rec := httptest.NewRecorder()
			req := httptest.NewRequest(http.MethodPost, "/internal/v1/voicegateway/hits", bytes.NewReader(body))
			req.Header.Set("Content-Type", "application/json")
			req.Header.Set("X-Internal-Token", config.DevInternalAPIToken)
			t0 := time.Now()
			engine.ServeHTTP(rec, req)
			lat[i] = time.Since(t0)
			if rec.Code != http.StatusOK {
				errs <- rec.Body.String()
			}
		}(i)
	}
	wg.Wait()
	close(errs)
	for msg := range errs {
		t.Fatalf("status not 200: %s", msg)
	}
	elapsed := time.Since(start)
	if elapsed > time.Second {
		t.Fatalf("100 concurrent posts took %s", elapsed)
	}
	var maxLat time.Duration
	for _, d := range lat {
		if d > maxLat {
			maxLat = d
		}
	}
	if maxLat > 50*time.Millisecond {
		t.Fatalf("max latency %s exceeds 50ms budget (in-process stand-in for p99)", maxLat)
	}
}
