// Package main verifies the three moat chains end to end through the real HTTP
// handlers, driven by mock iOS and mock voice-gateway payloads.
//
// The chains (doc 84 §五):
//
//  1. 救援 → 炼化:  mock gateway reports a session end carrying rescue_events;
//     the review job must hand those stuck points to the generator, and the
//     accepted refine cards must land in the corpus.
//  2. 实战命中回写:  mock gateway reports a B7 hit; the block must count it and
//     move its schedule (P1-2).
//  3. 闪测 → 申诉:  an iOS drill round, a failed answer, and the appeal that
//     puts the schedule back (E2).
//
// Only the two LLM calls are stubbed — everything else is the production path:
// routes, auth, stores, the review worker loop. That is the point: this smoke
// answers "are the paths connected", not "is the model good".
package main

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net"
	"net/http"
	"os"
	"strings"
	"sync"
	"time"

	"github.com/gin-gonic/gin"

	"github.com/FluentWork/fluentwork-backend/internal/account"
	"github.com/FluentWork/fluentwork-backend/internal/aicost"
	"github.com/FluentWork/fluentwork-backend/internal/config"
	"github.com/FluentWork/fluentwork-backend/internal/corpus"
	"github.com/FluentWork/fluentwork-backend/internal/drill"
	"github.com/FluentWork/fluentwork-backend/internal/httpserver"
	"github.com/FluentWork/fluentwork-backend/internal/reviewgen"
	"github.com/FluentWork/fluentwork-backend/internal/session"
	"github.com/FluentWork/fluentwork-backend/internal/topic"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "smoke-moat FAILED: %v\n", err)
		os.Exit(1)
	}
}

type evidence struct {
	Steps            []string `json:"steps"`
	RescueEventsSeen int      `json:"rescue_events_seen_by_generator"`
	RescuePaths      []string `json:"rescue_paths"`
	RescueAnchors    []string `json:"rescue_anchors"`
	RefineCards      int      `json:"refine_cards"`
	AcceptedBlocks   int      `json:"accepted_blocks"`
	HitRealUseCount  int      `json:"hit_real_use_count"`
	HitSuccessStreak int      `json:"hit_success_streak"`
	HitDuplicateKept int      `json:"hit_real_use_count_after_duplicate"`
	DrillRoundSize   int      `json:"drill_round_size"`
	JudgePassState   string   `json:"judge_pass_state"`
	JudgeFailStreak  int      `json:"judge_fail_streak"`
	AppealRestored   bool     `json:"appeal_restored"`
	AppealStreak     int      `json:"appeal_streak"`
	AppealDueBackTo  string   `json:"appeal_next_due_at"`
	// Chain 4 (T9/T10): the session-open suggestions and the refine-quality
	// reflux.
	RecommendationCount int    `json:"recommendation_count"`
	RecommendationWhy   string `json:"recommendation_reason"`
	FeedbackRecorded    bool   `json:"feedback_recorded"`
	// T8: the practice→reality summary.
	StatsBlocksUsed  int     `json:"stats_blocks_used"`
	StatsGreenBlocks int     `json:"stats_green_blocks"`
	StatsConversion  float64 `json:"stats_conversion_rate"`
	// P1-5: the roll-up is wired (0 rows: this smoke stubs both LLM calls).
	CostSummaryRows      []any `json:"-"`
	CostSummaryRowsCount int   `json:"cost_summary_rows"`
	ReviewReadyMS        int64 `json:"review_ready_ms"`
}

func run() error {
	if strings.TrimSpace(os.Getenv("APP_ENV")) == "" {
		_ = os.Setenv("APP_ENV", "development")
	}
	cfg := config.Load()
	if err := cfg.Validate(); err != nil {
		return fmt.Errorf("config: %w", err)
	}
	// WARN-level: the evidence block at the end is the point, and every request
	// this smoke makes would otherwise bury it.
	logger := slog.New(slog.NewTextHandler(os.Stderr, &slog.HandlerOptions{Level: slog.LevelWarn}))
	slog.SetDefault(logger)
	gin.SetMode(gin.ReleaseMode)

	accountStore, accountCloser, err := account.OpenStore(cfg, logger)
	if err != nil {
		return err
	}
	defer func() { _ = accountCloser() }()
	sessionStore, sessionCloser, err := session.OpenStore(cfg, logger)
	if err != nil {
		return err
	}
	defer func() { _ = sessionCloser() }()
	costStore, costCloser, err := aicost.OpenStore(cfg, logger)
	if err != nil {
		return err
	}
	defer func() { _ = costCloser() }()
	corpusStore, corpusCloser, err := corpus.OpenStore(cfg, logger)
	if err != nil {
		return err
	}
	defer func() { _ = corpusCloser() }()
	drillRecords, drillCloser, err := drill.OpenRecordStore(cfg, logger)
	if err != nil {
		return err
	}
	defer func() { _ = drillCloser() }()

	accountSvc := account.NewService(accountStore, session.Reassigner{Store: sessionStore}, cfg, logger)
	accountHandler := account.NewHandler(accountSvc)
	sessionSvc := session.NewService(sessionStore, cfg, logger)
	costSvc := aicost.NewService(costStore, logger)
	costHandler := aicost.NewHandler(costSvc)
	corpusSvc := corpus.NewService(corpusStore, logger)
	corpusHandler := corpus.NewHandler(corpusSvc, accountHandler)

	// The review/refine call is the one thing a repeatable smoke cannot take
	// from the network: a model that answers differently every run would make
	// every assertion flaky. This stub records what it was asked, and answers
	// with a document anchored to the rescue anchor it was given.
	generator := &recordingGenerator{}
	sessionSvc.SetReviewGenerator(generator)

	// The drill judge is stubbed the same way, with a scripted verdict.
	judge := &scriptedJudge{verdicts: []string{`{"pass":true,"judge_reason":"ok"}`, `{"pass":false,"judge_reason":"not equivalent"}`}}
	drillSvc := drill.NewService(corpusStore, drillRecords, &drill.LLMJudge{LLM: judge}, logger)
	drillSvc.SetConfig(drill.Config{
		Schedule:           corpus.ScheduleFromConfig(cfg),
		RoundSize:          cfg.DrillRoundSize,
		DailyNewBlockLimit: cfg.DrillDailyNewBlockLimit,
	})
	drillHandler := drill.NewHandler(drillSvc, accountHandler)

	// The topic handler is here for T8's stats route. Its generator has no LLM:
	// this smoke never asks a model for topic cards, and the stats it does read
	// come from the corpus the chains above built.
	topicStore := topic.NewMemoryStore()
	topicSvc := topic.NewService(topicStore, topic.NewGenerator(topicStore, nil, nil), logger)
	topicSvc.SetBlockLookup(corpusStore)
	topicHandler := topic.NewHandler(topicSvc, accountHandler)

	sessionHandler := session.NewHandler(sessionSvc, accountHandler)
	server := httpserver.New(cfg, logger, accountHandler, corpusHandler, nil, sessionHandler, costHandler,
		nil, drillHandler, nil, nil, topicHandler, accountStore.Ping)

	listener, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		return fmt.Errorf("listen: %w", err)
	}
	baseURL := "http://" + listener.Addr().String()
	httpServer := &http.Server{Handler: server.Handler(), ReadHeaderTimeout: 5 * time.Second}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	go func() { _ = httpServer.Serve(listener) }()
	go runWorker(ctx, sessionSvc, "smoke-moat-worker")
	defer func() {
		cancel()
		shutdownCtx, shutdownCancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer shutdownCancel()
		_ = httpServer.Shutdown(shutdownCtx)
	}()

	if err := waitHealthy(baseURL+"/healthz", 10*time.Second); err != nil {
		return err
	}

	result, err := exercise(baseURL, cfg.InternalAPIToken, generator, corpusStore)
	if err != nil {
		return err
	}
	encoded, err := json.MarshalIndent(result, "", "  ")
	if err != nil {
		return err
	}
	fmt.Println("=== moat chains smoke PASS ===")
	fmt.Println(string(encoded))
	if cfg.MySQLDSN == "" {
		fmt.Println("mode: in-process memory stores (HTTP handlers + worker loop co-located)")
	} else {
		fmt.Println("mode: MySQL-backed stores (MYSQL_DSN set)")
	}
	fmt.Println("stubbed: review generator + drill judge (deterministic); everything else is the production path")
	return nil
}

// recordingGenerator answers the review/refine call deterministically and keeps
// the request, which is how the smoke proves the rescue events reached D1.
type recordingGenerator struct {
	mu       sync.Mutex
	requests []reviewgen.Request
}

func (g *recordingGenerator) Generate(_ context.Context, req reviewgen.Request) (reviewgen.Result, error) {
	g.mu.Lock()
	g.requests = append(g.requests, req)
	g.mu.Unlock()

	// Anchor the block on the rescue anchor when there is one: PRD §5.2.2 says
	// that anchor is what the silent path contributes, and it is also what the
	// corpus accept step will carry.
	anchor := "I was going to say that the deploy is"
	expression := "The deploy is blocked on the migration."
	for _, event := range req.StuckEvents {
		if strings.TrimSpace(event.Anchor) != "" {
			anchor = event.Anchor
			if ladder := strings.TrimSpace(event.Ladder); ladder != "" {
				expression = ladder
			}
			break
		}
	}
	refine := map[string]any{"blocks": []map[string]any{{
		"intent_zh":        "说明部署被迁移卡住",
		"expression_en":    expression,
		"anchor_user_said": anchor,
		"scene_tag":        "standup",
		"function_tag":     "report",
	}}}
	review := map[string]any{
		"goal_achievement": map[string]any{"met": true, "note": "smoke"},
		"issues":           []any{},
		"suggestions":      []any{map[string]any{"text": "Keep the blocker explicit."}},
		"comparisons":      []any{map[string]any{"user": anchor, "better": expression}},
	}
	reviewRaw, err := json.Marshal(review)
	if err != nil {
		return reviewgen.Result{}, err
	}
	refineRaw, err := json.Marshal(refine)
	if err != nil {
		return reviewgen.Result{}, err
	}
	return reviewgen.Result{
		Review:    reviewRaw,
		Refine:    refineRaw,
		Generator: "smoke-moat-v1",
		Model:     "stub",
	}, nil
}

// scriptedJudge returns its verdicts in order, repeating the last one.
type scriptedJudge struct {
	mu       sync.Mutex
	verdicts []string
	calls    int
}

func (j *scriptedJudge) Complete(_ context.Context, _ string) (string, error) {
	j.mu.Lock()
	defer j.mu.Unlock()
	if len(j.verdicts) == 0 {
		return `{"pass":true,"judge_reason":"ok"}`, nil
	}
	idx := j.calls
	if idx >= len(j.verdicts) {
		idx = len(j.verdicts) - 1
	}
	j.calls++
	return j.verdicts[idx], nil
}

func runWorker(ctx context.Context, svc *session.Service, workerID string) {
	ticker := time.NewTicker(50 * time.Millisecond)
	defer ticker.Stop()
	for {
		select {
		case <-ctx.Done():
			return
		case <-ticker.C:
			if _, err := svc.ProcessNextJob(ctx, workerID); err != nil && !errors.Is(err, context.Canceled) {
				slog.Warn("smoke worker process", "err", err)
			}
		}
	}
}

func waitHealthy(url string, timeout time.Duration) error {
	deadline := time.Now().Add(timeout)
	for time.Now().Before(deadline) {
		res, err := http.Get(url) //nolint:gosec // local smoke only
		if err == nil {
			_ = res.Body.Close()
			if res.StatusCode == http.StatusOK {
				return nil
			}
		}
		time.Sleep(50 * time.Millisecond)
	}
	return fmt.Errorf("health check timed out: %s", url)
}

type header [2]string

func postJSON(client *http.Client, url, token string, payload any, headers ...header) (map[string]any, error) {
	body, err := json.Marshal(payload)
	if err != nil {
		return nil, err
	}
	req, err := http.NewRequest(http.MethodPost, url, bytes.NewReader(body))
	if err != nil {
		return nil, err
	}
	req.Header.Set("Content-Type", "application/json")
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for _, h := range headers {
		req.Header.Set(h[0], h[1])
	}
	return doJSON(client, req)
}

func getJSON(client *http.Client, url, token string, headers ...header) (map[string]any, error) {
	req, err := http.NewRequest(http.MethodGet, url, nil)
	if err != nil {
		return nil, err
	}
	if token != "" {
		req.Header.Set("Authorization", "Bearer "+token)
	}
	for _, h := range headers {
		req.Header.Set(h[0], h[1])
	}
	return doJSON(client, req)
}

func doJSON(client *http.Client, req *http.Request) (map[string]any, error) {
	res, err := client.Do(req)
	if err != nil {
		return nil, err
	}
	defer func() { _ = res.Body.Close() }()
	raw, err := io.ReadAll(io.LimitReader(res.Body, 1<<20))
	if err != nil {
		return nil, err
	}
	if res.StatusCode < 200 || res.StatusCode >= 300 {
		return nil, fmt.Errorf("%s %s -> %d: %s", req.Method, req.URL.Path, res.StatusCode, strings.TrimSpace(string(raw)))
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		return nil, fmt.Errorf("decode %s: %w", req.URL.Path, err)
	}
	return out, nil
}

// exercise drives the three chains in order, taking the same steps the iOS app
// and the gateway take. Every assertion is about wiring, not about model quality.
func exercise(
	baseURL, internalToken string,
	gen *recordingGenerator,
	store corpus.Store,
) (evidence, error) {
	client := &http.Client{Timeout: 15 * time.Second}
	ev := evidence{Steps: []string{}}
	step := func(name string) { ev.Steps = append(ev.Steps, name) }

	// --- mock iOS: guest session ---
	guest, err := postJSON(client, baseURL+"/api/v1/auth/guest", "", map[string]any{"device_id": "smoke-moat-device"})
	if err != nil {
		return ev, fmt.Errorf("guest auth: %w", err)
	}
	token, _ := guest["access_token"].(string)
	userID, _ := guest["user_id"].(string)
	if token == "" || userID == "" {
		return ev, fmt.Errorf("guest auth missing token/user: %#v", guest)
	}
	step("guest auth")

	created, err := postJSON(client, baseURL+"/api/v1/sessions", token, map[string]any{"scene_type": "standup"})
	if err != nil {
		return ev, fmt.Errorf("create session: %w", err)
	}
	sessionID, _ := created["session_id"].(string)
	if sessionID == "" {
		return ev, fmt.Errorf("create session missing id: %#v", created)
	}
	step("create session")

	if _, err := postJSON(client, baseURL+"/internal/v1/sessions/activate", "", map[string]any{
		"session_id": sessionID,
	}, header{"X-Internal-Token", internalToken}); err != nil {
		return ev, fmt.Errorf("activate: %w", err)
	}
	step("activate session")

	// --- mock gateway: the transcript plus the rescue ladders (P1-1) ---
	halfSentence := "I was going to say that the deploy is"
	if _, err := postJSON(client, baseURL+"/internal/v1/sessions/end", "", map[string]any{
		"session_id":   sessionID,
		"duration_sec": 42,
		"reason":       "user",
		"utterances": []map[string]any{
			{"seq": 1, "speaker": "ai", "text": "How is the release looking?"},
			{"seq": 2, "speaker": "user", "text": halfSentence},
			{"seq": 3, "speaker": "user", "text": "The deploy is blocked on the migration."},
		},
		"rescue_events": []map[string]any{
			{
				"seq": 1, "turn_id": "turn-1", "level": 3, "path": "incomplete",
				"ladder":      "The deploy is blocked on the migration.",
				"user_opened": true,
				"anchor":      halfSentence,
			},
			{"seq": 2, "turn_id": "turn-2", "level": 1, "path": "silent", "user_opened": false},
		},
	}, header{"X-Internal-Token", internalToken}); err != nil {
		return ev, fmt.Errorf("session end: %w", err)
	}
	step("session.end with rescue_events persisted + job enqueued")

	// --- chain 1: the review job must hand those stuck points to the generator ---
	ready, waited, err := waitForReview(client, baseURL, token, sessionID, 30*time.Second)
	if err != nil {
		return ev, err
	}
	ev.ReviewReadyMS = waited.Milliseconds()
	step("review ready")

	requests := gen.snapshot()
	if len(requests) == 0 {
		return ev, fmt.Errorf("generator was never called")
	}
	last := requests[len(requests)-1]
	ev.RescueEventsSeen = len(last.StuckEvents)
	for _, event := range last.StuckEvents {
		ev.RescuePaths = append(ev.RescuePaths, event.Path)
		ev.RescueAnchors = append(ev.RescueAnchors, event.Anchor)
	}
	if len(last.StuckEvents) != 2 {
		return ev, fmt.Errorf("generator saw %d stuck events, want 2: %+v", len(last.StuckEvents), last.StuckEvents)
	}
	if last.StuckEvents[0].Path != "incomplete" || last.StuckEvents[0].Anchor != halfSentence {
		return ev, fmt.Errorf("incomplete path lost its anchor: %+v", last.StuckEvents[0])
	}
	if last.StuckEvents[1].Path != "silent" || last.StuckEvents[1].Anchor != "" {
		return ev, fmt.Errorf("silent path must carry no anchor: %+v", last.StuckEvents[1])
	}
	step("generator received both rescue paths (§5.2.2)")

	cards, err := refineCards(ready)
	if err != nil {
		return ev, err
	}
	ev.RefineCards = len(cards)
	if len(cards) == 0 {
		return ev, fmt.Errorf("review payload carried no refine cards")
	}
	if anchor, _ := cards[0]["anchor_user_said"].(string); anchor != halfSentence {
		return ev, fmt.Errorf("refine anchor = %q, want the rescue anchor %q", anchor, halfSentence)
	}
	step("refine cards anchored on the rescue anchor")

	// --- mock iOS: 一键入库 ---
	accept, err := postJSON(client, baseURL+"/api/v1/corpus/blocks/batch-accept", token, map[string]any{
		"source_session_id": sessionID,
		"blocks":            cards,
	})
	if err != nil {
		return ev, fmt.Errorf("batch-accept: %w", err)
	}
	accepted, _ := accept["accepted_count"].(float64)
	ev.AcceptedBlocks = int(accepted)
	if ev.AcceptedBlocks == 0 {
		return ev, fmt.Errorf("batch-accept accepted nothing: %#v", accept)
	}
	step("corpus accepted the refine cards")

	blockID, err := firstBlockID(client, baseURL, token)
	if err != nil {
		return ev, err
	}
	step("corpus list shows the block")

	// --- chain 4: the session-open suggestions, and the quality reflux ---
	recommended, err := getJSON(client, baseURL+"/api/v1/corpus/recommendations?scene=standup", token)
	if err != nil {
		return ev, fmt.Errorf("recommendations: %w", err)
	}
	recItems, _ := recommended["items"].([]any)
	ev.RecommendationCount = len(recItems)
	if ev.RecommendationCount == 0 {
		return ev, fmt.Errorf("no recommendations for a corpus with one standup block: %#v", recommended)
	}
	firstRec, _ := recItems[0].(map[string]any)
	ev.RecommendationWhy = stringField(firstRec, "reason")
	if ev.RecommendationWhy != "scene_match" {
		return ev, fmt.Errorf("recommendation reason = %q, want a scene match", ev.RecommendationWhy)
	}
	step("corpus suggestions carry a reason")

	feedback, err := postJSON(client, baseURL+"/api/v1/corpus/blocks/"+blockID+"/feedback", token, map[string]any{
		"reason": "not_idiomatic",
	})
	if err != nil {
		return ev, fmt.Errorf("block feedback: %w", err)
	}
	ev.FeedbackRecorded, _ = feedback["recorded"].(bool)
	if !ev.FeedbackRecorded {
		return ev, fmt.Errorf("feedback not recorded: %#v", feedback)
	}
	step("refine-quality feedback recorded")

	// --- chain 2: mock gateway reports a B7 hit ---
	hitPayload := map[string]any{
		"user_id":    userID,
		"session_id": sessionID,
		"turn_id":    "turn-9",
		"hits": []map[string]any{
			{"block_id": blockID, "turn_id": "turn-9", "detected_at_ms": time.Now().UnixMilli()},
		},
	}
	if _, err := postJSON(client, baseURL+"/internal/v1/voicegateway/hits", "", hitPayload,
		header{"X-Internal-Token", internalToken}); err != nil {
		return ev, fmt.Errorf("hit report: %w", err)
	}
	afterHit, err := blockView(client, baseURL, token, blockID)
	if err != nil {
		return ev, err
	}
	ev.HitRealUseCount = intField(afterHit, "real_use_count")
	ev.HitSuccessStreak = intField(afterHit, "success_streak")
	if ev.HitRealUseCount != 1 || ev.HitSuccessStreak != 1 {
		return ev, fmt.Errorf("hit writeback missing: %#v", afterHit)
	}
	step("B7 hit counted (real_use_count + 视同成功)")

	// The same (session, turn, block) twice must not count twice.
	if _, err := postJSON(client, baseURL+"/internal/v1/voicegateway/hits", "", hitPayload,
		header{"X-Internal-Token", internalToken}); err != nil {
		return ev, fmt.Errorf("duplicate hit report: %w", err)
	}
	afterDuplicate, err := blockView(client, baseURL, token, blockID)
	if err != nil {
		return ev, err
	}
	ev.HitDuplicateKept = intField(afterDuplicate, "real_use_count")
	if ev.HitDuplicateKept != 1 {
		return ev, fmt.Errorf("duplicate hit double counted: %#v", afterDuplicate)
	}
	step("duplicate hit report is idempotent")

	// --- chain 3: flash drill, a failed answer, and the appeal (E2) ---
	// Fixture: make the block due, which is what tomorrow's session would find.
	if err := makeDue(context.Background(), store, userID, blockID, afterDuplicate); err != nil {
		return ev, err
	}
	round, err := getJSON(client, baseURL+"/api/v1/drill/round", token)
	if err != nil {
		return ev, fmt.Errorf("drill round: %w", err)
	}
	roundCards, _ := round["cards"].([]any)
	ev.DrillRoundSize = len(roundCards)
	if ev.DrillRoundSize == 0 {
		return ev, fmt.Errorf("drill round was empty: %#v", round)
	}
	step("drill round served the due block")

	passed, err := postJSON(client, baseURL+"/api/v1/drill/judge", token, map[string]any{
		"block_id": blockID, "asr_text": "The deploy is blocked on the migration.",
	})
	if err != nil {
		return ev, fmt.Errorf("drill judge (pass): %w", err)
	}
	ev.JudgePassState, _ = passed["state"].(string)
	if ev.JudgePassState != corpus.StateTraining {
		return ev, fmt.Errorf("expected training after a pass, got %#v", passed)
	}
	step("drill judge: pass advances the ladder")

	afterPass, err := blockView(client, baseURL, token, blockID)
	if err != nil {
		return ev, err
	}
	if err := makeDue(context.Background(), store, userID, blockID, afterPass); err != nil {
		return ev, err
	}
	// Read back after the fixture: this is the state the failed attempt will be
	// judged from, and therefore the state an appeal must restore.
	dueView, err := blockView(client, baseURL, token, blockID)
	if err != nil {
		return ev, err
	}
	dueBeforeFail := stringField(dueView, "next_due_at")

	failed, err := postJSON(client, baseURL+"/api/v1/drill/judge", token, map[string]any{
		"block_id": blockID, "asr_text": "uh, the deploy maybe later",
	})
	if err != nil {
		return ev, fmt.Errorf("drill judge (fail): %w", err)
	}
	ev.JudgeFailStreak = intField(failed, "success_streak")
	recordID := int64(intField(failed, "record_id"))
	if recordID == 0 {
		return ev, fmt.Errorf("judge response carried no record_id: %#v", failed)
	}
	if ev.JudgeFailStreak != 0 {
		return ev, fmt.Errorf("a failed attempt must zero the streak: %#v", failed)
	}
	step("drill judge: fail zeroes the streak")

	appeal, err := postJSON(client, baseURL+"/api/v1/drill/appeal", token, map[string]any{"record_id": recordID})
	if err != nil {
		return ev, fmt.Errorf("appeal: %w", err)
	}
	ev.AppealRestored, _ = appeal["restored"].(bool)
	ev.AppealStreak = intField(appeal, "success_streak")
	ev.AppealDueBackTo, _ = appeal["next_due_at"].(string)
	if !ev.AppealRestored {
		return ev, fmt.Errorf("appeal did not restore: %#v", appeal)
	}
	if ev.AppealStreak != 2 {
		return ev, fmt.Errorf("appeal streak = %d, want the pre-fail 2", ev.AppealStreak)
	}
	if ev.AppealDueBackTo != dueBeforeFail {
		return ev, fmt.Errorf("appeal next_due_at = %s, want the original %s", ev.AppealDueBackTo, dueBeforeFail)
	}
	step("appeal restored the schedule (结算照实算)")

	// --- T8: the summary the moat rests on reads back what the chains did ---
	// Last on purpose: it counts the hit, the drill attempts, and the appeal's
	// restore, so it has to run after all of them.
	stats, err := getJSON(client, baseURL+"/api/v1/topic-cards/stats?days=30", token)
	if err != nil {
		return ev, fmt.Errorf("practice stats: %w", err)
	}
	ev.StatsBlocksUsed = intField(stats, "blocks_used")
	ev.StatsGreenBlocks = intField(stats, "green_blocks")
	ev.StatsConversion, _ = stats["conversion_rate"].(float64)
	if ev.StatsBlocksUsed < 1 {
		return ev, fmt.Errorf("the hit from chain 2 must show up as a used block: %#v", stats)
	}
	step("practice stats count the used block")

	// --- P1-5: the cost roll-up answers, and says what its fen column is ---
	// No rows are expected here: this smoke stubs both LLM calls, so nothing has
	// been billed. The check is that the route, the internal token and the
	// honesty field are wired.
	summary, err := getJSON(client, baseURL+"/internal/v1/ai-cost-logs/summary?group_by=task_type", "",
		header{"X-Internal-Token", internalToken})
	if err != nil {
		return ev, fmt.Errorf("cost summary: %w", err)
	}
	if note := stringField(summary, "cost_caveat"); note == "" {
		return ev, fmt.Errorf("cost summary must carry its caveat: %#v", summary)
	}
	ev.CostSummaryRows, _ = summary["rows"].([]any)
	ev.CostSummaryRowsCount = len(ev.CostSummaryRows)
	step("cost roll-up answers with its caveat")

	return ev, nil
}

// makeDue rewrites a block's next_due_at into the past, standing in for the day
// that has to pass before a block is due again. It is the one place this smoke
// touches a store directly; everything else goes over HTTP.
func makeDue(ctx context.Context, store corpus.Store, userID, blockID string, view map[string]any) error {
	_, err := store.UpdateSchedule(ctx, userID, blockID,
		stringField(view, "state"), intField(view, "success_streak"),
		time.Now().UTC().Add(-time.Minute), time.Now().UTC())
	return err
}

func waitForReview(client *http.Client, baseURL, token, sessionID string, timeout time.Duration) (map[string]any, time.Duration, error) {
	started := time.Now()
	deadline := started.Add(timeout)
	for time.Now().Before(deadline) {
		poll, err := getJSON(client, baseURL+"/api/v1/sessions/"+sessionID+"/review", token)
		if err != nil {
			return nil, 0, fmt.Errorf("review poll: %w", err)
		}
		switch status, _ := poll["status"].(string); status {
		case session.ReviewPollReady:
			return poll, time.Since(started), nil
		case session.ReviewPollPending:
		default:
			return nil, 0, fmt.Errorf("review status = %q: %#v", status, poll)
		}
		time.Sleep(50 * time.Millisecond)
	}
	return nil, 0, fmt.Errorf("review not ready within %s", timeout)
}

// refineCards pulls the accepted-candidate blocks out of the ready review payload.
func refineCards(ready map[string]any) ([]map[string]any, error) {
	raw, err := json.Marshal(ready["review"])
	if err != nil {
		return nil, err
	}
	var doc struct {
		RefineCards []map[string]any `json:"refine_cards"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		return nil, fmt.Errorf("decode review payload: %w", err)
	}
	out := make([]map[string]any, 0, len(doc.RefineCards))
	for _, card := range doc.RefineCards {
		out = append(out, map[string]any{
			"intent_zh":        card["intent_zh"],
			"expression_en":    card["expression_en"],
			"anchor_user_said": card["anchor_user_said"],
			"scene_tag":        card["scene_tag"],
			"function_tag":     card["function_tag"],
		})
	}
	return out, nil
}

func firstBlockID(client *http.Client, baseURL, token string) (string, error) {
	list, err := getJSON(client, baseURL+"/api/v1/corpus/blocks", token)
	if err != nil {
		return "", fmt.Errorf("corpus list: %w", err)
	}
	items, _ := list["items"].([]any)
	if len(items) == 0 {
		return "", fmt.Errorf("corpus is empty: %#v", list)
	}
	first, _ := items[0].(map[string]any)
	id, _ := first["id"].(string)
	if id == "" {
		return "", fmt.Errorf("corpus item missing id: %#v", first)
	}
	return id, nil
}

func blockView(client *http.Client, baseURL, token, blockID string) (map[string]any, error) {
	list, err := getJSON(client, baseURL+"/api/v1/corpus/blocks", token)
	if err != nil {
		return nil, fmt.Errorf("corpus list: %w", err)
	}
	items, _ := list["items"].([]any)
	for _, item := range items {
		row, ok := item.(map[string]any)
		if !ok {
			continue
		}
		if id, _ := row["id"].(string); id == blockID {
			return row, nil
		}
	}
	return nil, fmt.Errorf("block %s not in corpus list", blockID)
}

func intField(row map[string]any, key string) int {
	value, _ := row[key].(float64)
	return int(value)
}

func stringField(row map[string]any, key string) string {
	value, _ := row[key].(string)
	return value
}

// snapshot returns a copy of the requests the generator has seen.
func (g *recordingGenerator) snapshot() []reviewgen.Request {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]reviewgen.Request(nil), g.requests...)
}
