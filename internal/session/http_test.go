package session_test

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
	"github.com/FluentWork/fluentwork-backend/internal/httpserver"
	"github.com/FluentWork/fluentwork-backend/internal/reviewgen"
	"github.com/FluentWork/fluentwork-backend/internal/session"
)

func setupServer(t *testing.T) (*httpserver.Server, *account.Service) {
	t.Helper()
	accountStore := account.NewMemoryStore()
	sessionStore := session.NewMemoryStore()
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
	accountSvc := account.NewService(accountStore, session.Reassigner{Store: sessionStore}, cfg, logger)
	accountHandler := account.NewHandler(accountSvc)
	sessionSvc := session.NewService(sessionStore, cfg, logger)
	sessionSvc.SetCostRecorder(aicost.NewService(aicost.NewMemoryStore(), logger))
	sessionHandler := session.NewHandler(sessionSvc, accountHandler)
	server := httpserver.New(cfg, logger, accountHandler, nil, nil, sessionHandler, accountStore.Ping)
	return server, accountSvc
}

func setupServerWithReviewGen(t *testing.T, gen session.ReviewGenerator) (*httpserver.Server, *session.Service) {
	t.Helper()
	accountStore := account.NewMemoryStore()
	sessionStore := session.NewMemoryStore()
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
	accountSvc := account.NewService(accountStore, session.Reassigner{Store: sessionStore}, cfg, logger)
	accountHandler := account.NewHandler(accountSvc)
	sessionSvc := session.NewService(sessionStore, cfg, logger)
	sessionSvc.SetCostRecorder(aicost.NewService(aicost.NewMemoryStore(), logger))
	sessionSvc.SetReviewGenerator(gen)
	sessionHandler := session.NewHandler(sessionSvc, accountHandler)
	return httpserver.New(cfg, logger, accountHandler, nil, nil, sessionHandler, accountStore.Ping), sessionSvc
}

func TestCreateSessionHTTPRequiresAuth(t *testing.T) {
	server, _ := setupServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions", bytes.NewReader([]byte(`{}`)))
	req.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
	var body apierr.Body
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.Code != "UNAUTHENTICATED" {
		t.Fatalf("code = %q", body.Code)
	}
}

func TestCreateSessionHTTPContract(t *testing.T) {
	server, _ := setupServer(t)

	guestRec := httptest.NewRecorder()
	guestReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/guest", bytes.NewReader([]byte(`{"device_id":"device-session-1"}`)))
	guestReq.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(guestRec, guestReq)
	if guestRec.Code != http.StatusOK {
		t.Fatalf("guest status = %d body = %s", guestRec.Code, guestRec.Body.String())
	}
	var guestBody account.TokenResponse
	if err := json.Unmarshal(guestRec.Body.Bytes(), &guestBody); err != nil {
		t.Fatalf("decode guest: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions", bytes.NewReader([]byte(`{"scene_type":"demo"}`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+guestBody.AccessToken)
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}

	var body session.CreateResponse
	if err := json.Unmarshal(rec.Body.Bytes(), &body); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if body.SessionID == "" || body.Ticket == "" || body.WSSURL == "" {
		t.Fatalf("unexpected body: %+v", body)
	}
	if body.TicketExpiresIn != 60 {
		t.Fatalf("ticket_expires_in = %d", body.TicketExpiresIn)
	}
	if body.Status != session.StatusCreated || body.SceneType != "demo" {
		t.Fatalf("unexpected metadata: %+v", body)
	}

	reviewRec := httptest.NewRecorder()
	reviewReq := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+body.SessionID+"/review", nil)
	reviewReq.Header.Set("Authorization", "Bearer "+guestBody.AccessToken)
	server.Handler().ServeHTTP(reviewRec, reviewReq)
	if reviewRec.Code != http.StatusOK {
		t.Fatalf("review status = %d body = %s", reviewRec.Code, reviewRec.Body.String())
	}
	var reviewBody session.ReviewPollResponse
	if err := json.Unmarshal(reviewRec.Body.Bytes(), &reviewBody); err != nil {
		t.Fatalf("decode review: %v", err)
	}
	if reviewBody.Status != session.ReviewPollPending || reviewBody.SessionID != body.SessionID {
		t.Fatalf("unexpected review body: %+v", reviewBody)
	}
}

func TestGetReviewHTTPRequiresAuth(t *testing.T) {
	server, _ := setupServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/s1/review", nil)
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
}

func TestGetReviewHTTPReadyReturnsFullModelContract(t *testing.T) {
	server, sessionSvc := setupServerWithReviewGen(t, fakeHTTPReviewGenerator{
		result: reviewgen.Result{
			Review:    json.RawMessage(`{"goal_achievement":{"met":true,"note":"ok"},"issues":[{"type":"idiomatic","original_quote":"sync up with the team","hint":"Prefer touch base."}],"suggestions":[{"text":"Use touch base."}],"comparisons":[{"user":"sync up with the team","better":"touch base with the team"},{"user":"I am blocked on the API review","better":"I'm blocked waiting on the API review"},{"user":"tomorrow","better":"tomorrow morning"}]}`),
			Refine:    json.RawMessage(`{"blocks":[{"intent_zh":"同步计划","expression_en":"I'll touch base with the team tomorrow.","anchor_user_said":"sync up with the team","scene_tag":"standup","function_tag":"commit"}]}`),
			Generator: "ark-review-refine-v1",
			Model:     "ep-review",
			TokensIn:  10,
			TokensOut: 20,
		},
	})

	guestRec := httptest.NewRecorder()
	guestReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/guest", bytes.NewReader([]byte(`{"device_id":"device-review-http-1"}`)))
	guestReq.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(guestRec, guestReq)
	var guestBody account.TokenResponse
	if err := json.Unmarshal(guestRec.Body.Bytes(), &guestBody); err != nil {
		t.Fatalf("decode guest: %v", err)
	}

	createRec := httptest.NewRecorder()
	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/sessions", bytes.NewReader([]byte(`{"scene_type":"standup"}`)))
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("Authorization", "Bearer "+guestBody.AccessToken)
	server.Handler().ServeHTTP(createRec, createReq)
	var created session.CreateResponse
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create: %v", err)
	}

	activateRec := httptest.NewRecorder()
	activateReq := httptest.NewRequest(http.MethodPost, "/internal/v1/sessions/activate", bytes.NewReader([]byte(`{"session_id":"`+created.SessionID+`"}`)))
	activateReq.Header.Set("Content-Type", "application/json")
	activateReq.Header.Set("X-Internal-Token", config.DevInternalAPIToken)
	server.Handler().ServeHTTP(activateRec, activateReq)
	if activateRec.Code != http.StatusOK {
		t.Fatalf("activate status = %d body = %s", activateRec.Code, activateRec.Body.String())
	}

	endRec := httptest.NewRecorder()
	endReq := httptest.NewRequest(http.MethodPost, "/internal/v1/sessions/end", bytes.NewReader([]byte(`{"session_id":"`+created.SessionID+`","duration_sec":18,"reason":"smoke","utterances":[{"seq":1,"speaker":"user","text":"I will sync up with the team tomorrow."},{"seq":2,"speaker":"user","text":"I am blocked on the API review."}]}`)))
	endReq.Header.Set("Content-Type", "application/json")
	endReq.Header.Set("X-Internal-Token", config.DevInternalAPIToken)
	server.Handler().ServeHTTP(endRec, endReq)
	if endRec.Code != http.StatusOK {
		t.Fatalf("end status = %d body = %s", endRec.Code, endRec.Body.String())
	}
	ok, err := sessionSvc.ProcessNextJob(context.Background(), "http-test-worker")
	if err != nil {
		t.Fatalf("ProcessNextJob: %v", err)
	}
	if !ok {
		t.Fatal("expected one job to process")
	}

	reviewRec := httptest.NewRecorder()
	reviewReq := httptest.NewRequest(http.MethodGet, "/api/v1/sessions/"+created.SessionID+"/review", nil)
	reviewReq.Header.Set("Authorization", "Bearer "+guestBody.AccessToken)
	server.Handler().ServeHTTP(reviewRec, reviewReq)
	if reviewRec.Code != http.StatusOK {
		t.Fatalf("review status = %d body = %s", reviewRec.Code, reviewRec.Body.String())
	}

	var reviewBody session.ReviewPollResponse
	if err := json.Unmarshal(reviewRec.Body.Bytes(), &reviewBody); err != nil {
		t.Fatalf("decode review body: %v", err)
	}
	if reviewBody.Status != session.ReviewPollReady {
		t.Fatalf("review status = %q", reviewBody.Status)
	}

	var payload map[string]any
	if err := json.Unmarshal(reviewBody.Review, &payload); err != nil {
		t.Fatalf("decode payload: %v", err)
	}
	for _, key := range []string{"transcript", "overview", "evaluation", "dual_column", "refine_cards", "review", "refine", "generator", "status"} {
		if _, ok := payload[key]; !ok {
			t.Fatalf("missing %s in review payload: %+v", key, payload)
		}
	}
	transcript, ok := payload["transcript"].([]any)
	if !ok || len(transcript) != 2 {
		t.Fatalf("unexpected transcript: %+v", payload["transcript"])
	}
	evaluation, ok := payload["evaluation"].([]any)
	if !ok || len(evaluation) != 3 {
		t.Fatalf("unexpected evaluation: %+v", payload["evaluation"])
	}
	dualColumn, ok := payload["dual_column"].([]any)
	if !ok || len(dualColumn) < 3 {
		t.Fatalf("unexpected dual_column: %+v", payload["dual_column"])
	}
	refineCards, ok := payload["refine_cards"].([]any)
	if !ok || len(refineCards) != 1 {
		t.Fatalf("unexpected refine_cards: %+v", payload["refine_cards"])
	}
}

func TestPostMessageHTTPRequiresAuth(t *testing.T) {
	server, _ := setupServer(t)
	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/s1/messages", bytes.NewReader([]byte(`{"text":"hi","channel":"text"}`)))
	req.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusUnauthorized {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
}

func TestPostMessageHTTPTextAndVoiceConflict(t *testing.T) {
	server, _ := setupServer(t)

	guestRec := httptest.NewRecorder()
	guestReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/guest", bytes.NewReader([]byte(`{"device_id":"device-msg-1"}`)))
	guestReq.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(guestRec, guestReq)
	var guestBody account.TokenResponse
	if err := json.Unmarshal(guestRec.Body.Bytes(), &guestBody); err != nil {
		t.Fatalf("decode guest: %v", err)
	}

	createRec := httptest.NewRecorder()
	createReq := httptest.NewRequest(http.MethodPost, "/api/v1/sessions", bytes.NewReader([]byte(`{}`)))
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("Authorization", "Bearer "+guestBody.AccessToken)
	server.Handler().ServeHTTP(createRec, createReq)
	if createRec.Code != http.StatusOK {
		t.Fatalf("create status = %d body = %s", createRec.Code, createRec.Body.String())
	}
	var created session.CreateResponse
	if err := json.Unmarshal(createRec.Body.Bytes(), &created); err != nil {
		t.Fatalf("decode create: %v", err)
	}

	voiceRec := httptest.NewRecorder()
	voiceReq := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+created.SessionID+"/messages", bytes.NewReader([]byte(`{"text":"hi"}`)))
	voiceReq.Header.Set("Content-Type", "application/json")
	voiceReq.Header.Set("Authorization", "Bearer "+guestBody.AccessToken)
	server.Handler().ServeHTTP(voiceRec, voiceReq)
	if voiceRec.Code != http.StatusConflict {
		t.Fatalf("voice status = %d body = %s", voiceRec.Code, voiceRec.Body.String())
	}
	var voiceErr apierr.Error
	if err := json.Unmarshal(voiceRec.Body.Bytes(), &voiceErr); err != nil {
		t.Fatalf("decode voice err: %v", err)
	}
	if voiceErr.Code != "CONFLICT" {
		t.Fatalf("voice err code = %s", voiceErr.Code)
	}

	okRec := httptest.NewRecorder()
	okReq := httptest.NewRequest(http.MethodPost, "/api/v1/sessions/"+created.SessionID+"/messages", bytes.NewReader([]byte(`{"text":"hello","channel":"text"}`)))
	okReq.Header.Set("Content-Type", "application/json")
	okReq.Header.Set("Authorization", "Bearer "+guestBody.AccessToken)
	server.Handler().ServeHTTP(okRec, okReq)
	if okRec.Code != http.StatusOK {
		t.Fatalf("text status = %d body = %s", okRec.Code, okRec.Body.String())
	}
	var msgBody session.PostMessageResponse
	if err := json.Unmarshal(okRec.Body.Bytes(), &msgBody); err != nil {
		t.Fatalf("decode message: %v", err)
	}
	if msgBody.SessionID != created.SessionID || msgBody.Channel != session.MessageChannelText || msgBody.Generator != "stub-text-v1" || msgBody.Reply == "" {
		t.Fatalf("unexpected message body: %+v", msgBody)
	}
}

func TestCreateSessionHTTPAllowsEmptyBody(t *testing.T) {
	server, _ := setupServer(t)
	guestRec := httptest.NewRecorder()
	guestReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/guest", bytes.NewReader([]byte(`{"device_id":"device-session-2"}`)))
	guestReq.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(guestRec, guestReq)
	var guestBody account.TokenResponse
	if err := json.Unmarshal(guestRec.Body.Bytes(), &guestBody); err != nil {
		t.Fatalf("decode guest: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions", http.NoBody)
	req.Header.Set("Authorization", "Bearer "+guestBody.AccessToken)
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusOK {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
}

type fakeHTTPReviewGenerator struct {
	result reviewgen.Result
	err    error
}

func (f fakeHTTPReviewGenerator) Generate(_ context.Context, _ reviewgen.Request) (reviewgen.Result, error) {
	if f.err != nil {
		return reviewgen.Result{}, f.err
	}
	return f.result, nil
}

func TestCreateSessionHTTPRejectsInvalidJSON(t *testing.T) {
	server, _ := setupServer(t)
	guestRec := httptest.NewRecorder()
	guestReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/guest", bytes.NewReader([]byte(`{"device_id":"device-session-3"}`)))
	guestReq.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(guestRec, guestReq)
	var guestBody account.TokenResponse
	if err := json.Unmarshal(guestRec.Body.Bytes(), &guestBody); err != nil {
		t.Fatalf("decode guest: %v", err)
	}

	rec := httptest.NewRecorder()
	req := httptest.NewRequest(http.MethodPost, "/api/v1/sessions", bytes.NewReader([]byte(`{`)))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+guestBody.AccessToken)
	server.Handler().ServeHTTP(rec, req)
	if rec.Code != http.StatusBadRequest {
		t.Fatalf("status = %d body = %s", rec.Code, rec.Body.String())
	}
}

func TestOpenAPIDiscoveryEndpoints(t *testing.T) {
	server, _ := setupServer(t)

	root := httptest.NewRecorder()
	server.Handler().ServeHTTP(root, httptest.NewRequest(http.MethodGet, "/", nil))
	if root.Code != http.StatusOK {
		t.Fatalf("root status = %d body = %s", root.Code, root.Body.String())
	}
	var discovery map[string]any
	if err := json.Unmarshal(root.Body.Bytes(), &discovery); err != nil {
		t.Fatalf("decode discovery: %v", err)
	}
	if discovery["openapi"] != "/openapi.yaml" || discovery["api_prefix"] != "/api/v1" {
		t.Fatalf("unexpected discovery: %+v", discovery)
	}

	spec := httptest.NewRecorder()
	server.Handler().ServeHTTP(spec, httptest.NewRequest(http.MethodGet, "/openapi.yaml", nil))
	if spec.Code != http.StatusOK {
		t.Fatalf("openapi status = %d", spec.Code)
	}
	body := spec.Body.String()
	if !bytes.Contains(spec.Body.Bytes(), []byte("openapi: 3.0.3")) {
		t.Fatalf("openapi missing version header: %s", body[:min(80, len(body))])
	}
	if !bytes.Contains(spec.Body.Bytes(), []byte("/sessions")) {
		t.Fatal("openapi missing /sessions path")
	}
	if !bytes.Contains(spec.Body.Bytes(), []byte("/sessions/{id}/review")) {
		t.Fatal("openapi missing /sessions/{id}/review path")
	}
	if !bytes.Contains(spec.Body.Bytes(), []byte("/sessions/{id}/messages")) {
		t.Fatal("openapi missing /sessions/{id}/messages path")
	}
}
