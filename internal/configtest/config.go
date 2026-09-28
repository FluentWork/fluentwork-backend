// Package configtest holds the one full config.Config a test needs in order to
// start an app-server: previously that fixture was copied into every
// http_test.go, and the copies had already drifted apart.
package configtest

import (
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/config"
)

// Config returns a configuration a test can hand to httpserver.New: every field
// Validate requires carries a value that is valid in the development
// environment, so a caller only spells out what its own test is about.
//
// The eight fields below were once written out in thirty-five copies across
// fourteen test files. Four different field sets had accumulated, and one
// duration had three spellings (SessionTicketTTL as time.Minute in 17 copies,
// 60*time.Second in 11 and 60_000_000_000 in 1), so "the config a test runs
// against" had no single answer. A caller that needs a different value
// overrides that field; a caller that needs fewer fields does not exist,
// because no mechanism in this repository rejects a Config for carrying extra
// ones.
func Config() config.Config {
	return config.Config{
		HTTPAddr:           ":0",
		AppEnv:             "development",
		AuthJWTSecret:      config.DevJWTSecret,
		AccessTokenTTL:     2 * time.Hour,
		RefreshTokenTTL:    24 * time.Hour,
		VoiceGatewayWSSURL: "ws://127.0.0.1:8081/v1/voice",
		SessionTicketTTL:   60 * time.Second,
		InternalAPIToken:   config.DevInternalAPIToken,
	}
}
