package topic

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"log/slog"
	"strings"
	"time"

	_ "github.com/go-sql-driver/mysql" // registers the mysql driver used by OpenStore

	"github.com/FluentWork/fluentwork-backend/internal/config"
)

// ErrNotFound means the card or streak row is missing.
var ErrNotFound = errors.New("topic: not found")

// ErrConflict means a checkin was already recorded.
var ErrConflict = errors.New("topic: conflict")

// Store persists topic cards, checkins, and streaks.
type Store interface {
	Ping(ctx context.Context) error
	ListTodayCards(ctx context.Context, userID string, forDate time.Time) ([]Card, error)
	HasCardsForDate(ctx context.Context, userID string, forDate time.Time) (bool, error)
	GetCard(ctx context.Context, cardID string) (Card, error)
	InsertCards(ctx context.Context, cards []Card) error
	MarkCheckedIn(ctx context.Context, cardID string, at time.Time) error
	InsertCheckin(ctx context.Context, row Checkin) error
	GetStreak(ctx context.Context, userID string) (Streak, error)
	UpsertStreak(ctx context.Context, streak Streak) error
	SoftDeleteAllForUser(ctx context.Context, userID string, at time.Time) (int, error)
	RestoreDeletedForUser(ctx context.Context, userID string) (int, error)
}

// OpenStore returns MySQL when MYSQL_DSN is set, otherwise memory.
func OpenStore(cfg config.Config, logger *slog.Logger) (Store, func() error, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if strings.TrimSpace(cfg.MySQLDSN) == "" {
		logger.Warn("MYSQL_DSN is empty; using in-memory topic store")
		return NewMemoryStore(), func() error { return nil }, nil
	}
	db, err := sql.Open("mysql", ensureParseTime(cfg.MySQLDSN))
	if err != nil {
		return nil, nil, fmt.Errorf("open mysql: %w", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	if err := db.PingContext(ctx); err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("ping mysql: %w", err)
	}
	return NewMySQLStore(db), db.Close, nil
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

func utcDate(t time.Time) time.Time {
	u := t.UTC()
	return time.Date(u.Year(), u.Month(), u.Day(), 0, 0, 0, 0, time.UTC)
}
