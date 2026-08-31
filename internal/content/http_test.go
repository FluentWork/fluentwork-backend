package content_test

import (
	"bytes"
	"encoding/json"
	"io"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"testing"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/account"
	"github.com/FluentWork/fluentwork-backend/internal/config"
	"github.com/FluentWork/fluentwork-backend/internal/content"
	"github.com/FluentWork/fluentwork-backend/internal/corpus"
	"github.com/FluentWork/fluentwork-backend/internal/httpserver"
	"github.com/FluentWork/fluentwork-backend/internal/session"
)

func setupServer(t *testing.T) (*httpserver.Server, *account.TokenResponse) {
	t.Helper()
	accountStore := account.NewMemoryStore()
	sessionStore := session.NewMemoryStore()
	corpusStore := corpus.NewMemoryStore()
	contentStore := content.NewMemoryStore()
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
		content.Reassigner{Store: contentStore},
	}, cfg, logger)
	accountHandler := account.NewHandler(accountSvc)
	corpusSvc := corpus.NewService(corpusStore, logger)
	corpusHandler := corpus.NewHandler(corpusSvc, accountHandler)
	contentSvc := content.NewService(contentStore, content.CorpusBlockSource{Store: corpusStore}, logger)
	contentHandler := content.NewHandler(contentSvc, accountHandler)
	server := httpserver.New(cfg, logger, accountHandler, corpusHandler, contentHandler, nil, accountStore.Ping)

	guestRec := httptest.NewRecorder()
	guestReq := httptest.NewRequest(http.MethodPost, "/api/v1/auth/guest", bytes.NewReader([]byte(`{"device_id":"device-daily-read-1"}`)))
	guestReq.Header.Set("Content-Type", "application/json")
	server.Handler().ServeHTTP(guestRec, guestReq)
	if guestRec.Code != http.StatusOK {
		t.Fatalf("guest status = %d body = %s", guestRec.Code, guestRec.Body.String())
	}
	var guestBody account.TokenResponse
	if err := json.Unmarshal(guestRec.Body.Bytes(), &guestBody); err != nil {
		t.Fatalf("decode guest: %v", err)
	}
	return server, &guestBody
}

func TestGetTodayAndFollowReadHTTPContract(t *testing.T) {
	server, guest := setupServer(t)

	todayRec := httptest.NewRecorder()
	todayReq := httptest.NewRequest(http.MethodGet, "/api/v1/daily-reads/today", nil)
	todayReq.Header.Set("Authorization", "Bearer "+guest.AccessToken)
	server.Handler().ServeHTTP(todayRec, todayReq)
	if todayRec.Code != http.StatusOK {
		t.Fatalf("today status = %d body = %s", todayRec.Code, todayRec.Body.String())
	}
	var today content.TodayPollResponse
	if err := json.Unmarshal(todayRec.Body.Bytes(), &today); err != nil {
		t.Fatalf("decode today: %v", err)
	}
	if today.Status != content.StatusReady || today.DailyRead == nil {
		t.Fatalf("unexpected today response: %+v", today)
	}
	if today.GenDate != time.Now().UTC().Format("2006-01-02") {
		t.Fatalf("gen_date = %q", today.GenDate)
	}

	followRec := httptest.NewRecorder()
	followReq := httptest.NewRequest(http.MethodPost, "/api/v1/daily-reads/"+today.DailyRead.ID+"/follow-read", bytes.NewReader([]byte(`{}`)))
	followReq.Header.Set("Content-Type", "application/json")
	followReq.Header.Set("Authorization", "Bearer "+guest.AccessToken)
	server.Handler().ServeHTTP(followRec, followReq)
	if followRec.Code != http.StatusOK {
		t.Fatalf("follow status = %d body = %s", followRec.Code, followRec.Body.String())
	}
	var follow content.FollowReadResponse
	if err := json.Unmarshal(followRec.Body.Bytes(), &follow); err != nil {
		t.Fatalf("decode follow: %v", err)
	}
	if !follow.Recorded || follow.Score != nil {
		t.Fatalf("unexpected follow response: %+v", follow)
	}
}
