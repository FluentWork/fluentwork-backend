// Package main seeds the dev environment with a starter set of phrase blocks so
// B12 hit detection has something to match against when exercising the full
// speaking-room → server → badge feedback loop locally.
//
// Usage:
//
//	./scripts/corpus-seed.sh              # targets http://127.0.0.1:8080
//	./scripts/corpus-seed.sh http://host:8080
//
// Each run uses a fresh source_session_id, so re-running adds another batch
// (batch-accept only dedupes exact (source_session_id, anchor_user_said)
// pairs). The list response is what matters for verification.
//
// This is dev-only: the seed list is hand-picked workplace English phrases
// chosen so that, when a developer says them in the speaking-room, the
// ASR transcript matches at least one of them and a B12 `feedback.badge`
// frame is emitted. It is NOT intended as production data.
package main

import (
	"bytes"
	"encoding/json"
	"flag"
	"fmt"
	"io"
	"net/http"
	"os"
	"strings"
	"time"
)

type seedBlock struct {
	IntentZH       string `json:"intent_zh"`
	ExpressionEN   string `json:"expression_en"`
	AnchorUserSaid string `json:"anchor_user_said"`
	SceneTag       string `json:"scene_tag"`
	FunctionTag    string `json:"function_tag"`
}

// devSeed is intentionally small and high-signal. Each anchor_user_said
// string is a phrase a non-native English speaker might actually say
// during a standup / 1on1 / review, and the ExpressionEN is the more
// idiomatic form the badge should highlight. The detector's job is to
// notice the anchor string inside the user's ASR transcript and surface
// the ExpressionEN.
var devSeed = []seedBlock{
	{
		IntentZH:       "说明阻塞点",
		ExpressionEN:   "I'm blocked on the API review.",
		AnchorUserSaid: "I am blocked on the API review.",
		SceneTag:       "standup",
		FunctionTag:    "report",
	},
	{
		IntentZH:       "把会议收尾",
		ExpressionEN:   "Let's wrap up.",
		AnchorUserSaid: "let's wrap up the meeting",
		SceneTag:       "review",
		FunctionTag:    "summarize",
	},
	{
		IntentZH:       "推动上线",
		ExpressionEN:   "Let's ship it.",
		AnchorUserSaid: "let's ship it",
		SceneTag:       "review",
		FunctionTag:    "commit",
	},
	{
		IntentZH:       "请求澄清",
		ExpressionEN:   "Could you clarify what you mean by that?",
		AnchorUserSaid: "could you clarify",
		SceneTag:       "1on1",
		FunctionTag:    "clarify",
	},
	{
		IntentZH:       "委婉拒绝延期",
		ExpressionEN:   "I'd rather not push the deadline.",
		AnchorUserSaid: "I don't want to push the deadline",
		SceneTag:       "1on1",
		FunctionTag:    "disagree",
	},
	{
		IntentZH:       "主动提议",
		ExpressionEN:   "How about we pair on this tomorrow?",
		AnchorUserSaid: "how about we pair on this tomorrow",
		SceneTag:       "casual",
		FunctionTag:    "propose",
	},
	{
		IntentZH:       "承认不确定",
		ExpressionEN:   "I'm not 100% sure yet, but I'll confirm by EOD.",
		AnchorUserSaid: "I'm not sure yet",
		SceneTag:       "standup",
		FunctionTag:    "defer",
	},
	{
		IntentZH:       "总结结论",
		ExpressionEN:   "Bottom line: we'll ship next Tuesday.",
		AnchorUserSaid: "bottom line",
		SceneTag:       "review",
		FunctionTag:    "summarize",
	},
	{
		IntentZH:       "请求反馈",
		ExpressionEN:   "Does that work for you?",
		AnchorUserSaid: "does that work for you",
		SceneTag:       "1on1",
		FunctionTag:    "ask",
	},
	{
		IntentZH:       "礼貌结束",
		ExpressionEN:   "Thanks for your time today.",
		AnchorUserSaid: "thanks for your time",
		SceneTag:       "casual",
		FunctionTag:    "agree",
	},
}

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

	token, err := issueGuestToken(client, *baseURL, *deviceID)
	if err != nil {
		return fmt.Errorf("guest auth: %w", err)
	}
	fmt.Println("  guest token issued")

	accepted, err := batchAccept(client, *baseURL, token, devSeed)
	if err != nil {
		return fmt.Errorf("batch-accept: %w", err)
	}
	fmt.Printf("  accepted %d / %d phrase blocks\n", accepted, len(devSeed))

	listed, err := listBlocks(client, *baseURL, token, "")
	if err != nil {
		return fmt.Errorf("list: %w", err)
	}
	fmt.Printf("  corpus now holds %d block(s)\n", listed)

	keywordHits, err := listBlocks(client, *baseURL, token, "wrap")
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

func issueGuestToken(client *http.Client, baseURL, deviceID string) (string, error) {
	body, _ := json.Marshal(map[string]any{"device_id": deviceID})
	req, _ := http.NewRequest(http.MethodPost, baseURL+"/api/v1/auth/guest", bytes.NewReader(body))
	req.Header.Set("Content-Type", "application/json")
	resp, err := client.Do(req)
	if err != nil {
		return "", err
	}
	defer func() { _ = resp.Body.Close() }()
	if resp.StatusCode < 200 || resp.StatusCode >= 300 {
		raw, _ := io.ReadAll(resp.Body)
		return "", fmt.Errorf("status %d: %s", resp.StatusCode, string(raw))
	}
	var out map[string]any
	if err := json.NewDecoder(resp.Body).Decode(&out); err != nil {
		return "", err
	}
	token, _ := out["access_token"].(string)
	if token == "" {
		return "", fmt.Errorf("missing access_token in response: %#v", out)
	}
	return token, nil
}

func batchAccept(client *http.Client, baseURL, token string, blocks []seedBlock) (int, error) {
	payload, _ := json.Marshal(map[string]any{
		"source_session_id": fmt.Sprintf("dev-seed-%d", time.Now().UTC().Unix()),
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
