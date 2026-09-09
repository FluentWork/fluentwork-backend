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

	"github.com/FluentWork/fluentwork-backend/internal/aicost"
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
	ListSessions(ctx context.Context, userID string, lastStartedAt *time.Time, lastID string, limit int) ([]Session, error)
	CreateTicket(ctx context.Context, ticket Ticket) error
	CreateSessionWithTicket(ctx context.Context, session Session, ticket Ticket) error
	GetTicketByHash(ctx context.Context, hash string) (Ticket, error)
	ConsumeTicket(ctx context.Context, hash string, at time.Time) (Ticket, error)
	MarkSessionActive(ctx context.Context, sessionID string, at time.Time) (Session, error)
	EndSession(ctx context.Context, sessionID string, durationSec int, utterances []Utterance, at time.Time) (Session, []Utterance, bool, error)
	ListUtterances(ctx context.Context, sessionID string) ([]Utterance, error)
	EnqueueJob(ctx context.Context, job Job) error
	HasSessionJob(ctx context.Context, sessionID, jobType string, statuses ...string) (bool, error)
	ClaimNextJob(ctx context.Context, workerID string, at time.Time) (Job, error)
	CompleteJob(ctx context.Context, jobID string, at time.Time) error
	FailJob(ctx context.Context, jobID string, at time.Time, errMsg string, retryDelay time.Duration) error
	MarkSessionReviewed(ctx context.Context, sessionID string, reviewJSON []byte, at time.Time) (Session, error)
	// MarkSessionReviewedWithCost writes review_json, status=reviewed, and one
	// ai_cost_logs row in a single database transaction so the two writes are
	// atomic. It uses the session store's own transaction; the cost record is
	// inserted into the same transaction as the review update.
	MarkSessionReviewedWithCost(ctx context.Context, sessionID string, reviewJSON []byte, at time.Time, costLog aicost.Log) (Session, error)
	ReassignUser(ctx context.Context, fromUserID, toUserID string) error
	SaveUtteranceEval(ctx context.Context, utteranceID string, evalJSON []byte) error
}

// OpenStore returns a MySQL store when MYSQL_DSN is set, otherwise memory.
// In MySQL mode it also opens an aicost.Store and wires its RecordCostTx into
// the returned MySQLStore, so MarkSessionReviewedWithCost can insert the cost
// ledger row inside its own transaction through the aicost package.
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

	// Open the cost ledger store so its RecordCostTx participates in the
	// session review transaction. Both stores share the same *sql.DB — they
	// only differ in which tables they own. Closing the session DB will
	// close the underlying pool; the aicost closer is invoked separately.
	costStore, costCloser, err := aicost.OpenStore(cfg, logger)
	if err != nil {
		_ = db.Close()
		return nil, nil, fmt.Errorf("open ai_cost store: %w", err)
	}

	sessionStore := NewMySQLStore(db)
	sessionStore.SetCostTx(costStore.RecordCostTx)

	closer := func() error {
		var firstErr error
		if err := costCloser(); err != nil {
			firstErr = err
		}
		if err := db.Close(); err != nil && firstErr == nil {
			firstErr = err
		}
		return firstErr
	}
	return sessionStore, closer, nil
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
