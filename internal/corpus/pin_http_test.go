package corpus_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"

	"github.com/FluentWork/fluentwork-backend/internal/account"
	"github.com/FluentWork/fluentwork-backend/internal/corpus"
)

func acceptOneBlock(t *testing.T, server http.Handler, token string) string {
	t.Helper()
	return acceptNamedBlock(t, server, token, "Let's ship it.")
}

func acceptNamedBlock(t *testing.T, server http.Handler, token, expr string) string {
	t.Helper()
	body, _ := json.Marshal(map[string]any{
		"source_session_id": "session-pin-1",
		"blocks": []map[string]string{{
			"intent_zh":        "推动上线",
			"expression_en":    expr,
			"anchor_user_said": strings.ToLower(expr),
			"scene_tag":        "review",
			"function_tag":     "commit",
		}},
	})
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/corpus/blocks/batch-accept", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	server.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("batch-accept status = %d body = %s", rec.Code, rec.Body.String())
	}
	var accepted corpus.BatchAcceptResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &accepted); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if len(accepted.Items) != 1 {
		t.Fatalf("accepted = %+v", accepted)
	}
	return accepted.Items[0].ID
}

func TestCorpus_Routes_Registered(t *testing.T) {
	server, _, _, guest := setupServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/corpus/blocks/missing/pin", bytes.NewReader([]byte(`{"pinned":true}`)))
	req.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("unauth PATCH /pin status = %d", rec.Code)
	}

	rec = httptest.NewRecorder()
	req = httptest.NewRequest(http.MethodPatch, "/api/v1/corpus/blocks/missing/favorite", bytes.NewReader([]byte(`{"favorite":true}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+guest.AccessToken)
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusNotFound {
		t.Fatalf("PATCH /favorite missing status = %d body = %s", rec.Code, rec.Body.String())
	}
}

func TestPatchPin_ThreeCardsAndUnpin(t *testing.T) {
	server, _, _, guest := setupServer(t)
	h := server.Handler()
	var ids []string
	for i := 0; i < 3; i++ {
		ids = append(ids, acceptNamedBlock(t, h, guest.AccessToken, "Let's ship it "+string(rune('A'+i))))
	}
	for _, id := range ids {
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(http.MethodPatch, "/api/v1/corpus/blocks/"+id+"/pin", bytes.NewReader([]byte(`{"pinned":true}`)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+guest.AccessToken)
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("pin %s status = %d body = %s", id, rec.Code, rec.Body.String())
		}
	}
	list := httptest.NewRecorder()
	lreq := httptest.NewRequest(http.MethodGet, "/api/v1/corpus/blocks?pinned_only=true", nil)
	lreq.Header.Set("Authorization", "Bearer "+guest.AccessToken)
	h.ServeHTTP(list, lreq)
	var listed corpus.ListBlocksResponse
	if err := json.Unmarshal(list.Body.Bytes(), &listed); err != nil {
		t.Fatalf("decode list: %v", err)
	}
	if len(listed.Items) != 3 {
		t.Fatalf("pinned_only len = %d", len(listed.Items))
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/corpus/blocks/"+ids[0]+"/pin", bytes.NewReader([]byte(`{"pinned":false}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+guest.AccessToken)
	h.ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("unpin status = %d", rec.Code)
	}
	var unpinned corpus.PhraseBlockView
	if err := json.Unmarshal(rec.Body.Bytes(), &unpinned); err != nil {
		t.Fatalf("decode unpin: %v", err)
	}
	if unpinned.PinnedAt != nil {
		t.Fatalf("expected unpin: %+v", unpinned)
	}
}

func TestPatchPin_MissingBodyField(t *testing.T) {
	server, _, _, guest := setupServer(t)
	id := acceptOneBlock(t, server.Handler(), guest.AccessToken)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/corpus/blocks/"+id+"/pin", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+guest.AccessToken)
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
}

func TestPatchPin_CrossUserForbidden(t *testing.T) {
	server, _, _, guest := setupServer(t)
	id := acceptOneBlock(t, server.Handler(), guest.AccessToken)

	other := httptest.NewRecorder()
	oreq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/guest", bytes.NewReader([]byte(`{"device_id":"device-corpus-other"}`)))
	oreq.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(other, oreq)
	var otherTok account.TokenResponse
	if err := json.Unmarshal(other.Body.Bytes(), &otherTok); err != nil {
		t.Fatalf("decode other: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPatch, "/api/v1/corpus/blocks/"+id+"/pin", bytes.NewReader([]byte(`{"pinned":true}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+otherTok.AccessToken)
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusForbidden {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
}

func TestPostFavorite_DeprecationHeadersAndMetric(t *testing.T) {
	before := corpus.DeprecatedPostFavoriteTotal()
	server, _, _, guest := setupServer(t)
	id := acceptOneBlock(t, server.Handler(), guest.AccessToken)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/corpus/blocks/"+id+"/favorite", bytes.NewReader([]byte(`{"is_favorite":true,"pinned":true}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+guest.AccessToken)
	req.Header.Set("User-Agent", "FluentWork-iOS")
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	if rec.Header().Get("Deprecation") != "true" {
		t.Fatalf("Deprecation = %q", rec.Header().Get("Deprecation"))
	}
	if rec.Header().Get("Sunset") != corpus.PostFavoriteSunsetHTTPDate {
		t.Fatalf("Sunset = %q", rec.Header().Get("Sunset"))
	}
	if !strings.Contains(rec.Header().Get("Link"), "/pin") {
		t.Fatalf("Link = %q", rec.Header().Get("Link"))
	}
	if corpus.DeprecatedPostFavoriteTotal() <= before {
		t.Fatal("expected metric increment")
	}
	metrics := httptest.NewRecorder()
	server.Handler().ServeHTTP(metrics, httptest.NewRequest(http.MethodGet, "/metrics", nil))
	body := metrics.Body.String()
	if !strings.Contains(body, "corpus_deprecated_post_favorite_total") {
		t.Fatalf("metrics missing counter: %s", body)
	}
}

func TestPatchAndPost_IdempotentSameState(t *testing.T) {
	server, _, _, guest := setupServer(t)
	h := server.Handler()
	id := acceptOneBlock(t, h, guest.AccessToken)
	auth := func(method, path, body string) corpus.PhraseBlockView {
		t.Helper()
		rec := httptest.NewRecorder()
		req := httptest.NewRequest(method, path, bytes.NewReader([]byte(body)))
		req.Header.Set("Content-Type", "application/json")
		req.Header.Set("Authorization", "Bearer "+guest.AccessToken)
		h.ServeHTTP(rec, req)
		if rec.Code != http.StatusOK {
			t.Fatalf("%s %s status = %d body = %s", method, path, rec.Code, rec.Body.String())
		}
		var view corpus.PhraseBlockView
		if err := json.Unmarshal(rec.Body.Bytes(), &view); err != nil {
			t.Fatalf("decode: %v", err)
		}
		return view
	}
	pin := auth(http.MethodPatch, "/api/v1/corpus/blocks/"+id+"/pin", `{"pinned":true}`)
	fav := auth(http.MethodPatch, "/api/v1/corpus/blocks/"+id+"/favorite", `{"favorite":true}`)
	if pin.PinnedAt == nil || !fav.IsFavorite || fav.PinnedAt == nil {
		t.Fatalf("patch overlay pin=%+v fav=%+v", pin, fav)
	}
	post := auth(http.MethodPost, "/api/v1/corpus/blocks/"+id+"/favorite", `{"is_favorite":true,"pinned":true}`)
	if !post.IsFavorite || post.PinnedAt == nil {
		t.Fatalf("post = %+v", post)
	}
	again := auth(http.MethodPatch, "/api/v1/corpus/blocks/"+id+"/pin", `{"pinned":true}`)
	if again.PinnedAt == nil || !again.IsFavorite {
		t.Fatalf("repeat patch = %+v", again)
	}
}
