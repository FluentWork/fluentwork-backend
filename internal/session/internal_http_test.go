package session_test

import (
	"bytes"
	"encoding/json"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/account"
	"github.com/FluentWork/fluentwork-backend/internal/config"
	"github.com/FluentWork/fluentwork-backend/internal/httpserver"
	"github.com/FluentWork/fluentwork-backend/internal/session"
)

func TestInternalConsumeTicket(t *testing.T) {
	t.Parallel()

	cfg := config.Config{
		HTTPAddr:           ":0",
		AppEnv:             "development",
		AuthJWTSecret:      config.DevJWTSecret,
		AccessTokenTTL:     time.Hour,
		RefreshTokenTTL:    24 * time.Hour,
		VoiceGatewayWSSURL: "ws://127.0.0.1:8081/v1/voice",
		SessionTicketTTL:   time.Minute,
		InternalAPIToken:   config.DevInternalAPIToken,
	}
	accountStore := account.NewMemoryStore()
	sessionStore := session.NewMemoryStore()
	accountSvc := account.NewService(accountStore, session.Reassigner{Store: sessionStore}, cfg, nil)
	accountHandler := account.NewHandler(accountSvc)
	sessionSvc := session.NewService(sessionStore, cfg, nil)
	sessionHandler := session.NewHandler(sessionSvc, accountHandler)
	srv := httpserver.New(cfg, nil, accountHandler, sessionHandler, nil)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	guestBody := bytes.NewBufferString(`{"device_id":"internal-ticket-device"}`)
	guestReq, err := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/auth/guest", guestBody)
	if err != nil {
		t.Fatalf("guest request: %v", err)
	}
	guestReq.Header.Set("Content-Type", "application/json")
	guestRes, err := http.DefaultClient.Do(guestReq)
	if err != nil {
		t.Fatalf("guest: %v", err)
	}
	defer guestRes.Body.Close()
	var guest map[string]any
	if err := json.NewDecoder(guestRes.Body).Decode(&guest); err != nil {
		t.Fatalf("decode guest: %v", err)
	}
	token, _ := guest["access_token"].(string)

	createReq, err := http.NewRequest(http.MethodPost, ts.URL+"/api/v1/sessions", bytes.NewBufferString(`{"scene_type":"demo"}`))
	if err != nil {
		t.Fatalf("create request: %v", err)
	}
	createReq.Header.Set("Content-Type", "application/json")
	createReq.Header.Set("Authorization", "Bearer "+token)
	createRes, err := http.DefaultClient.Do(createReq)
	if err != nil {
		t.Fatalf("create session: %v", err)
	}
	defer createRes.Body.Close()
	var created map[string]any
	if err := json.NewDecoder(createRes.Body).Decode(&created); err != nil {
		t.Fatalf("decode create: %v", err)
	}
	ticket, _ := created["ticket"].(string)
	sessionID, _ := created["session_id"].(string)
	if ticket == "" || sessionID == "" {
		t.Fatalf("unexpected create response: %#v", created)
	}

	consumeReq, err := http.NewRequest(
		http.MethodPost,
		ts.URL+"/internal/v1/tickets/consume",
		bytes.NewBufferString(`{"ticket":"`+ticket+`"}`),
	)
	if err != nil {
		t.Fatalf("consume request: %v", err)
	}
	consumeReq.Header.Set("Content-Type", "application/json")
	consumeReq.Header.Set("X-Internal-Token", config.DevInternalAPIToken)
	consumeRes, err := http.DefaultClient.Do(consumeReq)
	if err != nil {
		t.Fatalf("consume: %v", err)
	}
	defer consumeRes.Body.Close()
	if consumeRes.StatusCode != http.StatusOK {
		t.Fatalf("consume status = %d", consumeRes.StatusCode)
	}
	var consumed map[string]any
	if err := json.NewDecoder(consumeRes.Body).Decode(&consumed); err != nil {
		t.Fatalf("decode consume: %v", err)
	}
	if consumed["session_id"] != sessionID {
		t.Fatalf("session_id = %#v", consumed["session_id"])
	}

	// Second consume must fail (one-time ticket).
	replay, err := http.NewRequest(
		http.MethodPost,
		ts.URL+"/internal/v1/tickets/consume",
		bytes.NewBufferString(`{"ticket":"`+ticket+`"}`),
	)
	if err != nil {
		t.Fatalf("replay request: %v", err)
	}
	replay.Header.Set("Content-Type", "application/json")
	replay.Header.Set("X-Internal-Token", config.DevInternalAPIToken)
	replayRes, err := http.DefaultClient.Do(replay)
	if err != nil {
		t.Fatalf("replay: %v", err)
	}
	defer replayRes.Body.Close()
	if replayRes.StatusCode != http.StatusUnauthorized {
		t.Fatalf("replay status = %d", replayRes.StatusCode)
	}
}

func TestInternalConsumeTicketRequiresToken(t *testing.T) {
	t.Parallel()

	cfg := config.Config{
		HTTPAddr:           ":0",
		AppEnv:             "development",
		AuthJWTSecret:      config.DevJWTSecret,
		AccessTokenTTL:     time.Hour,
		RefreshTokenTTL:    24 * time.Hour,
		VoiceGatewayWSSURL: "ws://127.0.0.1:8081/v1/voice",
		SessionTicketTTL:   time.Minute,
		InternalAPIToken:   config.DevInternalAPIToken,
	}
	accountStore := account.NewMemoryStore()
	sessionStore := session.NewMemoryStore()
	accountSvc := account.NewService(accountStore, session.Reassigner{Store: sessionStore}, cfg, nil)
	accountHandler := account.NewHandler(accountSvc)
	sessionSvc := session.NewService(sessionStore, cfg, nil)
	sessionHandler := session.NewHandler(sessionSvc, accountHandler)
	srv := httpserver.New(cfg, nil, accountHandler, sessionHandler, nil)
	ts := httptest.NewServer(srv.Handler())
	t.Cleanup(ts.Close)

	req, err := http.NewRequest(http.MethodPost, ts.URL+"/internal/v1/tickets/consume", bytes.NewBufferString(`{"ticket":"x"}`))
	if err != nil {
		t.Fatalf("request: %v", err)
	}
	req.Header.Set("Content-Type", "application/json")
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("Do: %v", err)
	}
	defer res.Body.Close()
	if res.StatusCode != http.StatusUnauthorized {
		t.Fatalf("status = %d", res.StatusCode)
	}
}
