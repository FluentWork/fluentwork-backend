package corpus_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"

	"github.com/FluentWork/fluentwork-backend/internal/corpus"
)

// T9/T10 over HTTP: the session-open suggestion list and the "not idiomatic
// enough" button, which is the only signal prompt work can read back.
func TestRecommendationsAndFeedbackHTTPContract(t *testing.T) {
	server, _, _, guest := setupServer(t)

	accept := httptest.NewRecorder()
	acceptReq := httptest.NewRequest(http.MethodPost, "/api/v1/corpus/blocks/batch-accept",
		bytes.NewReader([]byte(`{"source_session_id":"s-1","blocks":[
			{"intent_zh":"说明部署被卡","expression_en":"The deploy is blocked on the migration.","anchor_user_said":"the deploy is","scene_tag":"standup","function_tag":"report"}
		]}`)))
	acceptReq.Header.Set("Authorization", "Bearer "+guest.AccessToken)
	acceptReq.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(accept, acceptReq)
	if accept.Code != http.StatusOK {
		t.Fatalf("accept status=%d body=%s", accept.Code, accept.Body.String())
	}
	var accepted corpus.BatchAcceptResponse
	if err := json.Unmarshal(accept.Body.Bytes(), &accepted); err != nil {
		t.Fatalf("decode accept: %v", err)
	}
	if len(accepted.Items) != 1 {
		t.Fatalf("accepted = %+v", accepted)
	}
	blockID := accepted.Items[0].ID

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/corpus/recommendations?scene=standup", nil)
	req.Header.Set("Authorization", "Bearer "+guest.AccessToken)
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("recommend status=%d body=%s", rec.Code, rec.Body.String())
	}
	var recommended corpus.RecommendationResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &recommended); err != nil {
		t.Fatalf("decode recommendations: %v", err)
	}
	if len(recommended.Items) != 1 || recommended.Items[0].ID != blockID {
		t.Fatalf("recommendations = %+v", recommended)
	}
	if recommended.Items[0].Reason != corpus.ReasonSceneMatch {
		t.Fatalf("reason = %q, want a scene match", recommended.Items[0].Reason)
	}

	fb := httptest.NewRecorder()
	fbReq := httptest.NewRequest(http.MethodPost, "/api/v1/corpus/blocks/"+blockID+"/feedback",
		bytes.NewReader([]byte(`{"reason":"not_idiomatic"}`)))
	fbReq.Header.Set("Authorization", "Bearer "+guest.AccessToken)
	fbReq.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(fb, fbReq)
	if fb.Code != http.StatusOK {
		t.Fatalf("feedback status=%d body=%s", fb.Code, fb.Body.String())
	}
	var feedback corpus.FeedbackResponse
	if err := json.Unmarshal(fb.Body.Bytes(), &feedback); err != nil {
		t.Fatalf("decode feedback: %v", err)
	}
	if !feedback.Recorded {
		t.Fatalf("feedback = %+v", feedback)
	}

	// A second tap of the same button is a no-op, and an unknown reason is a 400.
	fb2 := httptest.NewRecorder()
	fb2Req := httptest.NewRequest(http.MethodPost, "/api/v1/corpus/blocks/"+blockID+"/feedback",
		bytes.NewReader([]byte(`{"reason":"not_idiomatic"}`)))
	fb2Req.Header.Set("Authorization", "Bearer "+guest.AccessToken)
	fb2Req.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(fb2, fb2Req)
	if err := json.Unmarshal(fb2.Body.Bytes(), &feedback); err != nil {
		t.Fatalf("decode second feedback: %v", err)
	}
	if feedback.Recorded {
		t.Fatalf("second tap recorded: %+v", feedback)
	}

	bad := httptest.NewRecorder()
	badReq := httptest.NewRequest(http.MethodPost, "/api/v1/corpus/blocks/"+blockID+"/feedback",
		bytes.NewReader([]byte(`{"reason":"meh"}`)))
	badReq.Header.Set("Authorization", "Bearer "+guest.AccessToken)
	badReq.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(bad, badReq)
	if bad.Code != http.StatusBadRequest {
		t.Fatalf("open reason set accepted: %d", bad.Code)
	}

	summary := httptest.NewRecorder()
	summaryReq := httptest.NewRequest(http.MethodGet, "/api/v1/corpus/feedback", nil)
	summaryReq.Header.Set("Authorization", "Bearer "+guest.AccessToken)
	server.Handler().ServeHTTP(summary, summaryReq)
	if summary.Code != http.StatusOK {
		t.Fatalf("summary status=%d body=%s", summary.Code, summary.Body.String())
	}
	var summaryBody struct {
		Items []corpus.FeedbackReasonCount `json:"items"`
	}
	if err := json.Unmarshal(summary.Body.Bytes(), &summaryBody); err != nil {
		t.Fatalf("decode summary: %v", err)
	}
	var total int
	for _, item := range summaryBody.Items {
		total += item.Count
	}
	if total != 1 {
		t.Fatalf("summary = %+v, want exactly the one signal", summaryBody.Items)
	}
}
