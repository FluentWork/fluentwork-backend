package session

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

// ErrNotFound is returned when a session or ticket lookup misses.
var ErrNotFound = errors.New("session: not found")

// ErrTicketUsed is returned when a one-time ticket was already consumed.
var ErrTicketUsed = errors.New("session: ticket already used")

// ErrTicketExpired is returned when a ticket is past its expiry.
var ErrTicketExpired = errors.New("session: ticket expired")

// ErrConflict is returned when a session state transition is illegal.
var ErrConflict = errors.New("session: conflict")

// Store persists practice sessions and WSS tickets.
type Store interface {
	Ping(ctx context.Context) error
	CreateSession(ctx context.Context, session Session) error
	GetSession(ctx context.Context, id string) (Session, error)
	CreateTicket(ctx context.Context, ticket Ticket) error
	CreateSessionWithTicket(ctx context.Context, session Session, ticket Ticket) error
	GetTicketByHash(ctx context.Context, hash string) (Ticket, error)
	ConsumeTicket(ctx context.Context, hash string, at time.Time) (Ticket, error)
	MarkSessionActive(ctx context.Context, sessionID string, at time.Time) (Session, error)
	EndSession(ctx context.Context, sessionID string, durationSec int, utterances []Utterance, at time.Time) (Session, []Utterance, bool, error)
	ListUtterances(ctx context.Context, sessionID string) ([]Utterance, error)
	ReassignUser(ctx context.Context, fromUserID, toUserID string) error
}

// OpenStore returns a MySQL store when MYSQL_DSN is set, otherwise memory.
func OpenStore(cfg config.Config, logger *slog.Logger) (Store, func() error, error) {
	if logger == nil {
		logger = slog.Default()
	}
	if cfg.MySQLDSN == "" {
		logger.Warn("MYSQL_DSN is empty; using in-memory session store")
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
