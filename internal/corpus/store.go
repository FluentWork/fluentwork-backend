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
	Mode      CursorMode
	PinnedAt  *time.Time
	CreatedAt time.Time
	UpdatedAt time.Time
	ID        string
}

// Store persists phrase blocks for one user corpus.
type Store interface {
	Ping(ctx context.Context) error
	ListBlocks(ctx context.Context, filter ListFilter) ([]PhraseBlock, error)
	GetBlock(ctx context.Context, userID, blockID string) (PhraseBlock, error)
	SaveAcceptedBlocks(ctx context.Context, blocks []PhraseBlock) ([]PhraseBlock, error)
	UpdateBlock(ctx context.Context, block PhraseBlock) (PhraseBlock, error)
	SetFavorite(ctx context.Context, userID, blockID string, isFavorite bool, pinnedAt *time.Time, updatedAt time.Time) (PhraseBlock, error)
	SoftDeleteBlock(ctx context.Context, userID, blockID string, deletedAt time.Time) error
	ReassignUser(ctx context.Context, fromUserID, toUserID string) error
}

// OpenStore returns a MySQL store when MYSQL_DSN is set, otherwise memory.
func OpenStore(cfg config.Config, logger *slog.Logger) (Store, func() error, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.MySQLDSN == "" {
		logger.Warn("MYSQL_DSN is empty; using in-memory corpus store")
		return NewMemoryStore(), func() error { return nil }, nil
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
