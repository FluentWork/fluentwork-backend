package materials

import (
	"context"
	"errors"
	"strings"
	"testing"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/apierr"
	"github.com/FluentWork/fluentwork-backend/internal/corpus"
)

type stubLLM struct {
	body string
	err  error
}

func (s stubLLM) Complete(context.Context, string) (string, error) {
	return s.body, s.err
}

type boomBlocks struct{}

func (boomBlocks) SaveAcceptedBlocks(context.Context, []corpus.PhraseBlock) ([]corpus.PhraseBlock, error) {
	return nil, errors.New("db down")
}

func fiveBlockJSON() string {
	return `{"blocks":[
		{"intent_zh":"同步进度","expression_en":"I'll sync with the team.","scene_tag":"standup","function_tag":"report"},
		{"intent_zh":"澄清范围","expression_en":"Just to clarify the scope.","scene_tag":"review","function_tag":"clarify"},
		{"intent_zh":"提出方案","expression_en":"I'd suggest we try this.","scene_tag":"1on1","function_tag":"propose"},
		{"intent_zh":"表示同意","expression_en":"That works for me.","scene_tag":"casual","function_tag":"agree"},
		{"intent_zh":"请求帮助","expression_en":"Could you help me with this?","scene_tag":"interview","function_tag":"ask"}
	]}`
}

func TestStore_InsertGetMark(t *testing.T) {
	store := NewMemoryStore()
	now := time.Now().UTC()
	m := Material{ID: "m1", UserID: "u1", Kind: KindPaste, Content: "hello", RefineStatus: StatusQueued, CreatedAt: now, UpdatedAt: now}
	if err := store.InsertMaterial(context.Background(), m); err != nil {
		t.Fatal(err)
	}
	got, err := store.GetMaterial(context.Background(), "u1", "m1")
	if err != nil || got.Content != "hello" {
		t.Fatalf("get = %+v err=%v", got, err)
	}
	if err := store.MarkProcessing(context.Background(), "m1", now); err != nil {
		t.Fatal(err)
	}
	if err := store.MarkRefined(context.Background(), "m1", 3, "", now); err != nil {
		t.Fatal(err)
	}
	got, _ = store.GetMaterial(context.Background(), "u1", "m1")
	if got.RefineStatus != StatusReady || got.BlockCount != 3 {
		t.Fatalf("refined = %+v", got)
	}
	m2 := Material{ID: "m2", UserID: "u1", Kind: KindPaste, Content: "x", RefineStatus: StatusQueued, CreatedAt: now, UpdatedAt: now}
	_ = store.InsertMaterial(context.Background(), m2)
	_ = store.MarkProcessing(context.Background(), "m2", now)
	if err := store.MarkRefineFailed(context.Background(), "m2", ErrorParse, now); err != nil {
		t.Fatal(err)
	}
}

func TestCreate_PasteSentenceURLAndValidation(t *testing.T) {
	svc := NewService(NewMemoryStore(), corpus.NewMemoryStore(), stubLLM{body: fiveBlockJSON()}, nil)
	if _, err := svc.Create(context.Background(), "u1", CreateRequest{Kind: KindPaste, Content: strings.Repeat("word ", 200)}); err != nil {
		t.Fatalf("paste: %v", err)
	}
	if _, err := svc.Create(context.Background(), "u1", CreateRequest{Kind: KindSentence, Content: "I'll follow up tomorrow."}); err != nil {
		t.Fatalf("sentence: %v", err)
	}
	urlResp, err := svc.Create(context.Background(), "u1", CreateRequest{Kind: KindURL, Content: "https://example.com/doc"})
	if err != nil {
		t.Fatalf("url: %v", err)
	}
	got, _ := svc.Get(context.Background(), "u1", urlResp.MaterialID)
	if got.Content != URLPlaceholder {
		t.Fatalf("url content = %q", got.Content)
	}
	if _, err := svc.Create(context.Background(), "u1", CreateRequest{Kind: KindPaste, Content: ""}); err == nil {
		t.Fatal("empty")
	}
	if _, err := svc.Create(context.Background(), "u1", CreateRequest{Kind: KindPaste, Content: strings.Repeat("a", MaxContentLen+1)}); err == nil {
		t.Fatal("too long")
	}
}

func TestRefine_SuccessFiveBlocks(t *testing.T) {
	blocks := corpus.NewMemoryStore()
	store := NewMemoryStore()
	svc := NewService(store, blocks, stubLLM{body: fiveBlockJSON()}, nil)
	created, err := svc.Create(context.Background(), "u1", CreateRequest{Kind: KindPaste, Content: "standup notes"})
	if err != nil {
		t.Fatal(err)
	}
	if err := svc.Refine(context.Background(), created.MaterialID); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Get(context.Background(), "u1", created.MaterialID)
	if err != nil || got.RefineStatus != StatusReady || got.BlockCount != 5 {
		t.Fatalf("got = %+v err=%v", got, err)
	}
	listed, err := blocks.ListBlocks(context.Background(), corpus.ListFilter{UserID: "u1", Limit: 20})
	if err != nil || len(listed) != 5 {
		t.Fatalf("blocks n=%d err=%v", len(listed), err)
	}
}

func TestRefine_TimeoutAndParseError(t *testing.T) {
	svc := NewService(NewMemoryStore(), corpus.NewMemoryStore(), stubLLM{err: errors.New("timeout")}, nil)
	created, _ := svc.Create(context.Background(), "u1", CreateRequest{Kind: KindPaste, Content: "hi"})
	if err := svc.Refine(context.Background(), created.MaterialID); err != nil {
		t.Fatal(err)
	}
	got, _ := svc.Get(context.Background(), "u1", created.MaterialID)
	if got.RefineStatus != StatusFailed || got.ErrorCode != ErrorLLMTimeout {
		t.Fatalf("timeout got %+v", got)
	}
	if !strings.Contains(PrometheusMetrics(), "material_refine_timeout_total") {
		t.Fatal("missing timeout metric")
	}

	svc2 := NewService(NewMemoryStore(), corpus.NewMemoryStore(), stubLLM{body: "not-json"}, nil)
	created2, _ := svc2.Create(context.Background(), "u1", CreateRequest{Kind: KindPaste, Content: "hi"})
	_ = svc2.Refine(context.Background(), created2.MaterialID)
	got2, _ := svc2.Get(context.Background(), "u1", created2.MaterialID)
	if got2.RefineStatus != StatusFailed || got2.ErrorCode != ErrorParse {
		t.Fatalf("parse got %+v", got2)
	}
	if !strings.Contains(PrometheusMetrics(), "material_refine_parse_error_total") {
		t.Fatal("missing parse metric")
	}
}

func TestRefine_ZeroChunksReady(t *testing.T) {
	svc := NewService(NewMemoryStore(), corpus.NewMemoryStore(), stubLLM{body: `{"blocks":[]}`}, nil)
	created, _ := svc.Create(context.Background(), "u1", CreateRequest{Kind: KindPaste, Content: "zzz"})
	_ = svc.Refine(context.Background(), created.MaterialID)
	got, _ := svc.Get(context.Background(), "u1", created.MaterialID)
	if got.RefineStatus != StatusReady || got.ErrorCode != ErrorNoChunks || got.BlockCount != 0 {
		t.Fatalf("zero = %+v", got)
	}
}

func TestRefine_InsertBulkFailAndIdempotent(t *testing.T) {
	svc := NewService(NewMemoryStore(), boomBlocks{}, stubLLM{body: fiveBlockJSON()}, nil)
	created, _ := svc.Create(context.Background(), "u1", CreateRequest{Kind: KindPaste, Content: "hi"})
	if err := svc.Refine(context.Background(), created.MaterialID); err != nil {
		t.Fatal(err)
	}
	got, _ := svc.Get(context.Background(), "u1", created.MaterialID)
	if got.RefineStatus != StatusFailed || got.ErrorCode != ErrorDB {
		t.Fatalf("db fail = %+v", got)
	}

	svc2 := NewService(NewMemoryStore(), corpus.NewMemoryStore(), stubLLM{body: fiveBlockJSON()}, nil)
	ok, _ := svc2.Create(context.Background(), "u1", CreateRequest{Kind: KindPaste, Content: "hi"})
	_ = svc2.Refine(context.Background(), ok.MaterialID)
	if err := svc2.Refine(context.Background(), ok.MaterialID); err != nil {
		t.Fatalf("second refine: %v", err)
	}
	again, _ := svc2.Get(context.Background(), "u1", ok.MaterialID)
	if again.RefineStatus != StatusReady {
		t.Fatalf("idempotent status = %s", again.RefineStatus)
	}
	if err := svc2.store.MarkProcessing(context.Background(), ok.MaterialID, time.Now().UTC()); !errors.Is(err, ErrConflict) {
		t.Fatalf("ready→processing = %v", err)
	}
}

func TestGet_CrossUserAndSoftDelete(t *testing.T) {
	store := NewMemoryStore()
	svc := NewService(store, corpus.NewMemoryStore(), nil, nil)
	created, _ := svc.Create(context.Background(), "u1", CreateRequest{Kind: KindPaste, Content: "hi"})
	_, err := svc.Get(context.Background(), "other", created.MaterialID)
	var ae *apierr.Error
	if !errors.As(err, &ae) || ae.HTTPStatus != 403 {
		t.Fatalf("cross user = %v", err)
	}
	if _, err := store.SoftDeleteAllForUser(context.Background(), "u1", time.Now().UTC()); err != nil {
		t.Fatal(err)
	}
	_, err = svc.Get(context.Background(), "u1", created.MaterialID)
	if !errors.As(err, &ae) || ae.HTTPStatus != 404 {
		t.Fatalf("deleted = %v", err)
	}
	if _, err := store.RestoreDeletedForUser(context.Background(), "u1"); err != nil {
		t.Fatal(err)
	}
	got, err := svc.Get(context.Background(), "u1", created.MaterialID)
	if err != nil || got.ID != created.MaterialID {
		t.Fatalf("restored = %+v err=%v", got, err)
	}
}

func TestRefine_P95Budget(t *testing.T) {
	svc := NewService(NewMemoryStore(), corpus.NewMemoryStore(), stubLLM{body: fiveBlockJSON()}, nil)
	created, _ := svc.Create(context.Background(), "u1", CreateRequest{Kind: KindPaste, Content: strings.Repeat("word ", 100)})
	start := time.Now()
	if err := svc.Refine(context.Background(), created.MaterialID); err != nil {
		t.Fatal(err)
	}
	if time.Since(start) > 35*time.Second {
		t.Fatalf("refine too slow: %s", time.Since(start))
	}
}
