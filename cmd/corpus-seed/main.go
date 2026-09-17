// Package main seeds the dev environment with a starter set of phrase blocks so
// B12 hit detection has something to match against when exercising the full
// speaking-room → server → badge feedback loop locally.
//
// Usage:
//
//	./scripts/corpus-seed.sh              # targets http://127.0.0.1:8080
//	./scripts/corpus-seed.sh http://host:8080
//
// Re-running is idempotent: source_session_id is fixed and batch-accept
// dedupes exact (source_session_id, anchor_user_said) pairs, so dev-up.sh can
// safely call this on every startup without duplicating phrase blocks.
//
// The phrase list lives in internal/corpus/starter.go, shared with the
// development auto-provisioner so the two cannot drift. It is NOT production
// data.
//
// Note that app-server now provisions the same starter corpus automatically for
// a guest's first session in development, so this command is only needed to
// seed a *specific* device explicitly.
package main

import (
	"bytes"
	"database/sql"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/corpus"

	_ "github.com/go-sql-driver/mysql" // register driver for dev session upsert
)

const devSourceSessionID = "dev-corpus-seed-session"

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "corpus-seed FAILED: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	baseURL := flag.String("base-url", defaultBaseURL(), "app-server base URL, e.g. http://127.0.0.1:8080")
	deviceID := flag.String("device-id", "corpus-seed-dev-device", "device id used for guest auth")
	flag.Parse()

	fmt.Printf("Seeding corpus against %s with device_id=%s\n", *baseURL, *deviceID)
	client := &http.Client{Timeout: 10 * time.Second}

	guest, err := issueGuest(client, *baseURL, *deviceID)
	if err != nil {
		return fmt.Errorf("guest auth: %w", err)
	}
	fmt.Println("  guest token issued")

	sessionID := devSourceSessionID
	dsn := strings.TrimSpace(os.Getenv("MYSQL_DSN"))
	if dsn == "" {
		// In-memory dev store: every session is ephemeral, so create one via
		// the API; idempotency only matters for MySQL mode.
		sessionID, err = createDevSession(client, *baseURL, guest.AccessToken)
		if err != nil {
			return fmt.Errorf("create dev session: %w", err)
		}
	} else if err := ensureDevSourceSession(dsn, guest.UserID); err != nil {
		return fmt.Errorf("ensure dev source session: %w", err)
	}
	fmt.Printf("  dev session created: %s\n", sessionID)

	accepted, err := batchAccept(client, *baseURL, guest.AccessToken, sessionID, corpus.StarterBlocks())
	if err != nil {
		return fmt.Errorf("batch-accept: %w", err)
	}
	fmt.Printf("  accepted %d / %d phrase blocks\n", accepted, len(corpus.StarterBlocks()))

	listed, err := listBlocks(client, *baseURL, guest.AccessToken, "")
	if err != nil {
		return fmt.Errorf("list: %w", err)
	}
	fmt.Printf("  corpus now holds %d block(s)\n", listed)

	keywordHits, err := listBlocks(client, *baseURL, guest.AccessToken, "wrap")
	if err != nil {
		return fmt.Errorf("keyword search: %w", err)
	}
	fmt.Printf("  keyword search 'wrap' returned %d block(s)\n", keywordHits)

	fmt.Println("=== corpus-seed PASS ===")
	return nil
}

func defaultBaseURL() string {
	if v := strings.TrimSpace(os.Getenv("APP_BASE_URL")); v != "" {
		return v
	}
	return "http://127.0.0.1:8080"
}

type guestIdentity struct {
	AccessToken string
	UserID      string
}

func issueGuest(client *http.Client, baseURL, deviceID string) (guestIdentity, error) {
	body, _ := json.Marshal(map[string]any{"device_id": deviceID})
	req, _ := http.NewRequest(http.MethodPost, baseURL+"/api/v1/auth/guest", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return guestIdentity{}, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(resp.Body)
		return guestIdentity{}, fmt.Errorf("status %d: %s", resp.StatusCode, string(raw))
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return guestIdentity{}, err
	}
	token, _ := out["access_token"].(string)
	userID, _ := out["user_id"].(string)
	if token == "" || userID == "" {
		return guestIdentity{}, fmt.Errorf("missing access_token/user_id in response: %#v", out)
	}
	return guestIdentity{AccessToken: token, UserID: userID}, nil
}

func ensureDevSourceSession(dsn, userID string) error {
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return err
	}
	defer func() { _ = db.Close() }()
	now := time.Now().UTC()
	_, err = db.Exec(`
		INSERT IGNORE INTO practice_sessions
			(id, user_id, material_id, scene_type, status, duration_sec,
			 created_at, updated_at)
		VALUES (?, ?, NULL, 'review', 'ended', 0, ?, ?)`,
		devSourceSessionID, userID, now, now,
	)
	return err
}

func createDevSession(client *http.Client, baseURL, token string) (string, error) {
	payload, _ := json.Marshal(map[string]any{"scene_type": "review"})
	req, _ := http.NewRequest(http.MethodPost, baseURL+"/api/v1/sessions", bytes.NewReader(payload))
	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("status %d: %s", resp.StatusCode, string(raw))
	}
	var out struct {
		SessionID string `json:"session_id"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	if out.SessionID == "" {
		return "", fmt.Errorf("missing session_id in response")
	}
	return out.SessionID, nil
}

func batchAccept(client *http.Client, baseURL, token, sessionID string, blocks []corpus.BatchAcceptBlock) (int, error) {
	payload, _ := json.Marshal(map[string]any{
		"source_session_id": sessionID,
		"blocks":            blocks,
	})
	req, _ := http.NewRequest(http.MethodPost, baseURL+"/api/v1/corpus/blocks/batch-accept", bytes.NewReader(payload))
	req.Header.Set("Authorization", "Bearer "+token)
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("status %d: %s", resp.StatusCode, string(raw))
	}
	var out struct {
		AcceptedCount int `json:"accepted_count"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, err
	}
	return out.AcceptedCount, nil
}

func listBlocks(client *http.Client, baseURL, token, kw string) (int, error) {
	url := baseURL + "/api/v1/corpus/blocks?limit=100"
	if kw != "" {
		url += "&kw=" + kw
	}
	req, _ := http.NewRequest(http.MethodGet, url, nil)
	req.Header.Set("Authorization", "Bearer "+token)
	resp, err := client.Do(req)
	if err != nil {
		return 0, err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(resp.Body)
		return 0, fmt.Errorf("status %d: %s", resp.StatusCode, string(raw))
	}
	var out struct {
		Items []any `json:"items"`
	}
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return 0, err
	}
	return len(out.Items), nil
}
