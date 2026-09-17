// Package config loads app-server runtime configuration from the environment.
package config

import (
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"
)

// DevJWTSecret is the local-only signing secret. Production must override it.
const DevJWTSecret = "fluentwork-dev-jwt-secret-change-me!!"

// DevInternalAPIToken is the local-only shared secret between app-server and voice-gateway.
const DevInternalAPIToken = "fluentwork-dev-internal-token-change-me!!"

const (
	defaultHTTPAddr           = ":8080"
	defaultAccessTokenTTL     = 2 * time.Hour
	defaultRefreshTokenTTL    = 30 * 24 * time.Hour
	defaultVoiceGatewayWSSURL = "ws://127.0.0.1:8081/v1/voice"
	defaultSessionTicketTTL   = 60 * time.Second
)

// E3 drill-scheduling defaults (PRD §5.3.2). These are the PRD ladder; the
// DRILL_* environment variables override them without a redeploy.
const (
	defaultDrillPromoteStreak           = 3
	defaultDrillTrainingInterval        = 24 * time.Hour
	defaultDrillAutomatedInterval       = 7 * 24 * time.Hour
	defaultDrillAutomatedReviewInterval = 30 * 24 * time.Hour
	defaultDrillFailInterval            = time.Hour
	defaultDrillRoundSize               = 10
	// defaultDrillDailyNewBlockLimit of 0 means "no cap": every 灰 block that is
	// due may enter a round, which is the behaviour before E3 existed.
	defaultDrillDailyNewBlockLimit = 0
	// defaultDrillOverdueWindow is 83_ §2.1's "只保留最近 3 天".
	defaultDrillOverdueWindow = 72 * time.Hour
	// defaultDrillJudgeTimeout mirrors drill.JudgeTimeout; the drill package owns
	// the reasoning, this keeps config free of that import cycle.
	defaultDrillJudgeTimeout = 6 * time.Second
	// defaultArkHTTPTimeout is the shipping bound on one provider call.
	defaultArkHTTPTimeout = 30 * time.Second
	// defaultArkThinking keeps the chain of thought off: the deployed endpoints
	// answer json_object workloads in seconds with it disabled and hang for
	// minutes with it on (see reviewgen's history).
	defaultArkThinking = "disabled"
	// defaultTopicMinBlocks is the PRD §7.8 H1 threshold (话术块 ≥ 20).
	defaultTopicMinBlocks = 20
)

// Config holds process settings for app-server.
type Config struct {
	HTTPAddr           string
	AppEnv             string
	MySQLDSN           string
	AuthJWTSecret      string
	AccessTokenTTL     time.Duration
	RefreshTokenTTL    time.Duration
	VoiceGatewayWSSURL string
	SessionTicketTTL   time.Duration
	InternalAPIToken   string
	ArkBaseURL         string
	ArkAPIKey          string
	ArkReviewRefineEP  string
	ArkDailyReadEP     string
	ArkTopicCardEP     string
	ArkHitMatchEP      string
	ArkDrillJudgeEP    string
	ArkTextDegradeEP   string
	// ArkThinking is the vendor's chain-of-thought switch: "disabled" (default)
	// or "auto". It exists because the setting is vendor-specific — a different
	// provider simply ignores it.
	ArkThinking string
	// ArkHTTPTimeout bounds one provider call. 30s is the default a chat
	// completion normally needs; a caller that sends a large prompt (a whole
	// corpus, say) can raise it without a code change.
	ArkHTTPTimeout time.Duration
	// ArkPricingFile, when set, replaces the built-in model price table
	// (doc 79). P2-2's "以账单为准" then lands as a data change; a file that does
	// not parse fails startup instead of silently billing at the old rates.
	ArkPricingFile string

	// Drill scheduling ladder (PRD §5.3.2, E3). One ladder serves both the
	// flash drill's judging and the B7 hit writeback: corpus.ScheduleFromConfig
	// maps these onto corpus.Schedule so the two can never diverge.
	DrillPromoteStreak           int
	DrillTrainingInterval        time.Duration
	DrillAutomatedInterval       time.Duration
	DrillAutomatedReviewInterval time.Duration
	DrillFailInterval            time.Duration
	// DrillRoundSize is the default cards per round when the client sends no
	// size (E1: 一轮 10 题).
	DrillRoundSize int
	// DrillDailyNewBlockLimit caps how many 灰 blocks enter rounds per UTC day.
	// 0 disables the cap (每日新块释放上限, E3).
	DrillDailyNewBlockLimit int

	// DrillJudgeTimeout bounds one flash-drill judgement. The 1.5s design budget
	// timed out on every measured call (86_ F1); the default is the measured
	// maximum with headroom.
	DrillJudgeTimeout time.Duration
	// DrillOverdueWindow is how far overdue a block may be before a round folds
	// it back to "due now" (83_ §2.1 风险 2: 过期任务不累积). 0 disables the
	// sweep.
	DrillOverdueWindow time.Duration
	// TopicMinBlocks is H1's 语料库阈值: below it a learner gets no topic cards,
	// because there is nothing of their own to ground one on (PRD §7.8).
	TopicMinBlocks int
}

// Load reads configuration from environment variables.
// APP_ENV has no implicit default: local scripts must set it explicitly so a
// forgotten production deploy cannot silently treat the process as development.
// Development secrets are only auto-filled when APP_ENV is an explicit local value.
func Load() Config {
	appEnv := strings.TrimSpace(os.Getenv("APP_ENV"))
	return Config{
		HTTPAddr:           envOr("HTTP_ADDR", defaultHTTPAddr),
		AppEnv:             appEnv,
		MySQLDSN:           strings.TrimSpace(os.Getenv("MYSQL_DSN")),
		AuthJWTSecret:      secretOr("AUTH_JWT_SECRET", DevJWTSecret, appEnv),
		AccessTokenTTL:     durationOr("AUTH_ACCESS_TTL", defaultAccessTokenTTL),
		RefreshTokenTTL:    durationOr("AUTH_REFRESH_TTL", defaultRefreshTokenTTL),
		VoiceGatewayWSSURL: envOr("VOICE_GATEWAY_WSS_URL", defaultVoiceGatewayWSSURL),
		SessionTicketTTL:   durationOr("SESSION_TICKET_TTL", defaultSessionTicketTTL),
		InternalAPIToken:   secretOr("INTERNAL_API_TOKEN", DevInternalAPIToken, appEnv),
		ArkBaseURL:         envOr("ARK_BASE_URL", "https://ark.cn-beijing.volces.com/api/v3"),
		ArkAPIKey:          firstNonEmpty(strings.TrimSpace(os.Getenv("ARK_API_KEY")), strings.TrimSpace(os.Getenv("ARK_API_KEY_DEV"))),
		ArkReviewRefineEP:  strings.TrimSpace(os.Getenv("ARK_EP_REVIEW_REFINE")),
		ArkDailyReadEP:     strings.TrimSpace(os.Getenv("ARK_EP_DAILY_READ")),
		ArkTopicCardEP:     strings.TrimSpace(os.Getenv("ARK_EP_TOPIC_CARD")),
		ArkHitMatchEP:      strings.TrimSpace(os.Getenv("ARK_EP_HIT_MATCH")),
		ArkDrillJudgeEP:    strings.TrimSpace(os.Getenv("ARK_EP_DRILL_JUDGE")),
		ArkTextDegradeEP:   strings.TrimSpace(os.Getenv("ARK_EP_TEXT_DEGRADE")),
		ArkPricingFile:     strings.TrimSpace(os.Getenv("ARK_PRICING_FILE")),
		ArkHTTPTimeout:     durationOr("ARK_HTTP_TIMEOUT", defaultArkHTTPTimeout),
		ArkThinking:        envOr("ARK_THINKING", defaultArkThinking),

		DrillPromoteStreak:           intOr("DRILL_PROMOTE_STREAK", defaultDrillPromoteStreak),
		DrillTrainingInterval:        durationOr("DRILL_TRAINING_INTERVAL", defaultDrillTrainingInterval),
		DrillAutomatedInterval:       durationOr("DRILL_AUTOMATED_INTERVAL", defaultDrillAutomatedInterval),
		DrillAutomatedReviewInterval: durationOr("DRILL_AUTOMATED_REVIEW_INTERVAL", defaultDrillAutomatedReviewInterval),
		DrillFailInterval:            durationOr("DRILL_FAIL_INTERVAL", defaultDrillFailInterval),
		DrillRoundSize:               intOr("DRILL_ROUND_SIZE", defaultDrillRoundSize),
		DrillDailyNewBlockLimit:      intOr("DRILL_DAILY_NEW_BLOCK_LIMIT", defaultDrillDailyNewBlockLimit),

		DrillOverdueWindow: durationOr("DRILL_OVERDUE_WINDOW", defaultDrillOverdueWindow),
		DrillJudgeTimeout:  durationOr("DRILL_JUDGE_TIMEOUT", defaultDrillJudgeTimeout),

		TopicMinBlocks: intOr("TOPIC_MIN_BLOCKS", defaultTopicMinBlocks),
	}
}

// intOr reads an integer setting. Unparseable or empty keeps the default;
// negatives are passed through so Validate can reject them loudly rather than
// have a typo quietly become a default.
func intOr(key string, fallback int) int {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	parsed, err := strconv.Atoi(raw)
	if err != nil {
		return fallback
	}
	return parsed
}

// IsProduction reports whether the process is running in production.
func (c Config) IsProduction() bool {
	return strings.EqualFold(c.AppEnv, "production")
}

// IsDevelopment reports whether the process is an explicitly configured local/test environment.
func (c Config) IsDevelopment() bool {
	return isDevelopmentEnv(c.AppEnv)
}

// Validate checks required production and auth constraints.
func (c Config) Validate() error {
	if strings.TrimSpace(c.HTTPAddr) == "" {
		return fmt.Errorf("HTTP_ADDR is required")
	}
	if strings.TrimSpace(c.AppEnv) == "" {
		return fmt.Errorf("APP_ENV is required")
	}
	if len(c.AuthJWTSecret) < 32 {
		return fmt.Errorf("AUTH_JWT_SECRET must be at least 32 characters")
	}
	if !c.IsDevelopment() && c.AuthJWTSecret == DevJWTSecret {
		return fmt.Errorf("AUTH_JWT_SECRET must be set outside development environments")
	}
	if c.IsProduction() && c.MySQLDSN == "" {
		return fmt.Errorf("MYSQL_DSN is required in production")
	}
	if c.AccessTokenTTL <= 0 {
		return fmt.Errorf("AUTH_ACCESS_TTL must be positive")
	}
	if c.RefreshTokenTTL <= 0 {
		return fmt.Errorf("AUTH_REFRESH_TTL must be positive")
	}
	if strings.TrimSpace(c.VoiceGatewayWSSURL) == "" {
		return fmt.Errorf("VOICE_GATEWAY_WSS_URL is required")
	}
	if c.SessionTicketTTL <= 0 {
		return fmt.Errorf("SESSION_TICKET_TTL must be positive")
	}
	if len(strings.TrimSpace(c.InternalAPIToken)) < 16 {
		return fmt.Errorf("INTERNAL_API_TOKEN must be at least 16 characters")
	}
	if !c.IsDevelopment() && (c.InternalAPIToken == "" || c.InternalAPIToken == DevInternalAPIToken) {
		return fmt.Errorf("INTERNAL_API_TOKEN must be set outside development environments")
	}
	if err := c.validateDrillSchedule(); err != nil {
		return err
	}
	return nil
}

// validateDrillSchedule rejects a ladder that cannot schedule anything. A
// negative duration would make every block due immediately, which reads as
// "working" while quietly destroying the spacing the mechanism rests on.
//
// Zero is allowed and means "unset": the corpus ladder normalizes zero fields
// to the PRD defaults, so a Config literal that never mentions drills keeps the
// documented behaviour.
func (c Config) validateDrillSchedule() error {
	switch {
	case c.DrillPromoteStreak < 0:
		return fmt.Errorf("DRILL_PROMOTE_STREAK must not be negative")
	case c.DrillTrainingInterval < 0:
		return fmt.Errorf("DRILL_TRAINING_INTERVAL must not be negative")
	case c.DrillAutomatedInterval < 0:
		return fmt.Errorf("DRILL_AUTOMATED_INTERVAL must not be negative")
	case c.DrillAutomatedReviewInterval < 0:
		return fmt.Errorf("DRILL_AUTOMATED_REVIEW_INTERVAL must not be negative")
	case c.DrillFailInterval < 0:
		return fmt.Errorf("DRILL_FAIL_INTERVAL must not be negative")
	case c.DrillRoundSize < 0:
		return fmt.Errorf("DRILL_ROUND_SIZE must not be negative")
	case c.DrillDailyNewBlockLimit < 0:
		return fmt.Errorf("DRILL_DAILY_NEW_BLOCK_LIMIT must not be negative")
	case c.TopicMinBlocks < 0:
		return fmt.Errorf("TOPIC_MIN_BLOCKS must not be negative")
	case c.DrillOverdueWindow < 0:
		return fmt.Errorf("DRILL_OVERDUE_WINDOW must not be negative")
	case c.DrillJudgeTimeout < 0:
		return fmt.Errorf("DRILL_JUDGE_TIMEOUT must not be negative")
	}
	return nil
}

func envOr(key, fallback string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value == "" {
		return fallback
	}
	return value
}

func secretOr(key, devFallback, appEnv string) string {
	value := strings.TrimSpace(os.Getenv(key))
	if value != "" {
		return value
	}
	if isDevelopmentEnv(appEnv) {
		return devFallback
	}
	return ""
}

func isDevelopmentEnv(appEnv string) bool {
	switch strings.ToLower(strings.TrimSpace(appEnv)) {
	case "development", "dev", "test", "local":
		return true
	default:
		return false
	}
}

func durationOr(key string, fallback time.Duration) time.Duration {
	raw := strings.TrimSpace(os.Getenv(key))
	if raw == "" {
		return fallback
	}
	parsed, err := time.ParseDuration(raw)
	if err != nil {
		return fallback
	}
	return parsed
}

func firstNonEmpty(values ...string) string {
	for _, value := range values {
		if strings.TrimSpace(value) != "" {
			return strings.TrimSpace(value)
		}
	}
	return ""
}
