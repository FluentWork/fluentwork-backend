package account

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

// ErrNotFound is returned when a user or token lookup misses.
var ErrNotFound = errors.New("account: not found")

// ErrDuplicateDeviceID is returned when a guest insert races on device_id.
var ErrDuplicateDeviceID = errors.New("account: duplicate device_id")

// ErrDuplicateEmail is returned when a registered insert hits the email unique key.
//
// 它与 `ErrDuplicateDeviceID` 分开，不是为了好看：两个唯一键撞了之后的**处理不同** ——
// 设备号撞了是并发重试（再查一次沿用），邮箱撞了是**注册失败**（这个人已经在了）。
var ErrDuplicateEmail = errors.New("account: duplicate email")

// Store persists users and refresh tokens.
type Store interface {
	Ping(ctx context.Context) error
	CreateUser(ctx context.Context, user User) error
	GetUser(ctx context.Context, id string) (User, error)
	GetActiveByDeviceID(ctx context.Context, deviceID string) (User, error)
	// GetActiveByEmail 按邮箱取活跃用户（账号密码登录用）。
	//
	// 邮箱**以小写存储**（注册与登录都先 normalize），所以这里不需要大小写不敏感的排序规则。
	GetActiveByEmail(ctx context.Context, email string) (User, error)
	MarkMerged(ctx context.Context, guestID, targetID, deviceID string, at time.Time) error
	FindGuestMergedInto(ctx context.Context, targetID string) (User, error)
	ReplaceRefreshToken(ctx context.Context, token RefreshToken) error
	GetRefreshToken(ctx context.Context, hash string) (RefreshToken, error)
	DeleteRefreshTokensForUser(ctx context.Context, userID string) error
	MarkDeleted(ctx context.Context, userID string, at, tombstoneAt time.Time) error
	ClearDeleted(ctx context.Context, userID string) error
	InsertTombstone(ctx context.Context, row Tombstone) error
	DeleteTombstonesForUser(ctx context.Context, userID string) (int, error)
	InsertAudit(ctx context.Context, row AuditLog) error
}

// OpenStore returns a MySQL store when MYSQL_DSN is set, otherwise memory.
func OpenStore(cfg config.Config, logger *slog.Logger) (Store, func() error, error) {
	if cfg.MySQLDSN == "" {
		logger.Warn("MYSQL_DSN is empty; using in-memory account store")
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
