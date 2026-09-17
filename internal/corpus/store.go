// Package corpus stores phrase blocks and exposes corpus HTTP APIs.
package corpus

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql" // registers mysql driver used by OpenStore

	"github.com/FluentWork/fluentwork-backend/internal/config"
)

// ErrNotFound means the requested phrase block does not exist for the user.
var ErrNotFound = errors.New("corpus: not found")

// ListFilter scopes phrase block queries.
type ListFilter struct {
	UserID       string
	SceneTag     string
	FunctionTag  string
	Keyword      string
	FavoriteOnly bool
	PinnedOnly   bool
	Incremental  bool
	UpdatedAfter *time.Time
	After        *ListCursor
	Limit        int
}

// CursorMode distinguishes browse pagination from incremental sync pagination.
type CursorMode string

const (
	// CursorModeBrowse is the classic keyset pagination cursor for visible list browsing.
	CursorModeBrowse CursorMode = "browse"
	// CursorModeDelta is the ascending updated_at cursor used by incremental sync.
	CursorModeDelta CursorMode = "delta"
)

// ListCursor is the keyset pagination cursor for block lists.
type ListCursor struct {
	Mode       CursorMode
	PinnedAt   *time.Time
	CreatedAt  time.Time
	UpdatedAt  time.Time
	IsPinned   bool
	IsFavorite bool
	ID         string
}

// Store persists phrase blocks for one user corpus.
type Store interface {
	Ping(ctx context.Context) error
	ListBlocks(ctx context.Context, filter ListFilter) ([]PhraseBlock, error)
	GetBlock(ctx context.Context, userID, blockID string) (PhraseBlock, error)
	PeekBlock(ctx context.Context, blockID string) (PhraseBlock, error)
	SaveAcceptedBlocks(ctx context.Context, blocks []PhraseBlock) ([]PhraseBlock, error)
	UpdateBlock(ctx context.Context, block PhraseBlock) (PhraseBlock, error)
	SetFavorite(ctx context.Context, userID, blockID string, isFavorite bool, pinnedAt *time.Time, updatedAt time.Time) (PhraseBlock, error)
	SoftDeleteBlock(ctx context.Context, userID, blockID string, deletedAt time.Time) error
	ReassignUser(ctx context.Context, fromUserID, toUserID string) error
	RecordHits(ctx context.Context, userID, sessionID, turnID string, hits []Hit) (int, error)
	ListSessionHits(ctx context.Context, sessionID string) ([]RecentHit, error)
	ListDueBlocks(ctx context.Context, userID string, now time.Time, states []string, limit int) ([]PhraseBlock, error)
	// SweepOverdue folds blocks that fell due before `dueBefore` forward to
	// `at`, so a learner returning after a break meets one normal queue instead
	// of a pile of 20-day-old debt (83_ §2.1 风险 2: 过期任务不累积). It reports
	// how many rows moved.
	SweepOverdue(ctx context.Context, userID string, dueBefore, at time.Time) (int, error)
	UpdateSchedule(ctx context.Context, userID, blockID, state string, successStreak int, nextDueAt, updatedAt time.Time) (PhraseBlock, error)
	SoftDeleteAllForUser(ctx context.Context, userID string, deletedAt time.Time) (int, error)
	RestoreDeletedForUser(ctx context.Context, userID string) (int, error)
	// RecordRealUses credits confirmed real-world uses of the learner's blocks:
	// a provenance row per (block, source, ref), the counters, and the same
	// 视同成功 reschedule a B7 hit gets (PRD §5.2.3). Idempotent per ref, so a
	// repeated checkin cannot inflate anything. Returns how many blocks were
	// newly credited.
	RecordRealUses(ctx context.Context, userID string, blockIDs []string, source, refID string, at time.Time) (int, error)
	// CountRealUsesBySource returns how many uses each source contributed since
	// a time — the L1/L2 split (86_ M9).
	CountRealUsesBySource(ctx context.Context, userID string, since time.Time) (map[string]int, error)
	// SaveFeedback records one quality signal, idempotent on
	// (user_id, block_id, reason). It reports whether this call created the row.
	SaveFeedback(ctx context.Context, feedback Feedback) (bool, error)
	// CountFeedback returns how many live rows carry each reason, for the
	// metrics endpoint.
	CountFeedback(ctx context.Context, userID string) (map[string]int, error)
	// SoftDeleteFeedbackForUser and RestoreFeedbackForUser keep A4's wipe
	// complete: feedback text is the user's own judgement, so it goes with the
	// account.
	SoftDeleteFeedbackForUser(ctx context.Context, userID string, deletedAt time.Time) (int, error)
	RestoreFeedbackForUser(ctx context.Context, userID string) (int, error)
}

// OpenStore returns a MySQL store when MYSQL_DSN is set, otherwise memory.
func OpenStore(cfg config.Config, logger *slog.Logger) (Store, func() error, error) {
	if logger == nil {
		logger = slog.Default()
	}
	schedule := ScheduleFromConfig(cfg)
	if cfg.MySQLDSN == "" {
		logger.Warn("MYSQL_DSN is empty; using in-memory corpus store")
		store := NewMemoryStore()
		store.SetSchedule(schedule)
		return store, func() error { return nil }, nil
	}
	db, err := sql.Open("mysql", ensureParseTime(cfg.MySQLDSN))
	if err != nil {
		return nil, nil, fmt.Errorf("open mysql: %w", err)
	}
	db.SetMaxOpenConns(20)
	db.SetMaxIdleConns(10)
	db.SetConnMaxLifetime(30 * time.Minute)

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("ping mysql: %w", err)
	}
	store := NewMySQLStore(db)
	store.SetSchedule(schedule)
	return store, db.Close, nil
}

// ScheduleFromConfig maps the DRILL_* settings onto the ladder (E3). Fields the
// environment leaves unset carry their config default, so this is always "the
// server's ladder" and never a partially filled one.
func ScheduleFromConfig(cfg config.Config) Schedule {
	return Schedule{
		PromoteStreak:           cfg.DrillPromoteStreak,
		TrainingInterval:        cfg.DrillTrainingInterval,
		AutomatedInterval:       cfg.DrillAutomatedInterval,
		AutomatedReviewInterval: cfg.DrillAutomatedReviewInterval,
		FailInterval:            cfg.DrillFailInterval,
	}.Normalize()
}

func ensureParseTime(dsn string) string {
	lower := strings.ToLower(dsn)
	if !strings.Contains(lower, "parsetime=") {
		if strings.Contains(dsn, "?") {
			dsn += "&parseTime=true"
		} else {
			dsn += "?parseTime=true"
		}
	}
	if !strings.Contains(strings.ToLower(dsn), "loc=") {
		if strings.Contains(dsn, "?") {
			dsn += "&loc=UTC"
		} else {
			dsn += "?loc=UTC"
		}
	}
	return dsn
}
