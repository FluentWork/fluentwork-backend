package materials

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

// ErrNotFound means the material is missing for this user.
var ErrNotFound = errors.New("materials: not found")

// ErrConflict means an illegal refine_status transition.
var ErrConflict = errors.New("materials: conflict")

// Store persists materials.
type Store interface {
	Ping(ctx context.Context) error
	InsertMaterial(ctx context.Context, m Material) error
	GetMaterial(ctx context.Context, userID, materialID string) (Material, error)
	MarkProcessing(ctx context.Context, materialID string, at time.Time) error
	MarkRefined(ctx context.Context, materialID string, blockCount int, errorCode string, at time.Time) error
	MarkRefineFailed(ctx context.Context, materialID, errorCode string, at time.Time) error
	SoftDeleteAllForUser(ctx context.Context, userID string, at time.Time) (int, error)
	RestoreDeletedForUser(ctx context.Context, userID string) (int, error)
}

// OpenStore returns MySQL when MYSQL_DSN is set, otherwise memory.
func OpenStore(cfg config.Config, logger *slog.Logger) (Store, func() error, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if strings.TrimSpace(cfg.MySQLDSN) == "" {
		logger.Warn("MYSQL_DSN is empty; using in-memory materials store")
		return NewMemoryStore(), func() error { return nil }, nil
	}
	db, err := sql.Open("mysql", cfg.MySQLDSN)
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
