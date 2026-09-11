package session

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"strings"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/aicost"
)

// MySQLStore persists sessions and tickets in MySQL 8.
type MySQLStore struct {
	db *sql.DB
	// costTx inserts one ai_cost_logs row into the same database transaction
	// that MarkSessionReviewedWithCost opens. It is wired by session.OpenStore
	// to aicost.MySQLStore.RecordCostTx so the INSERT SQL lives in exactly
	// one place (the aicost package). MarkSessionReviewedWithCost refuses to
	// run without it wired — protecting the "review + cost must commit
	// together" invariant from accidental silent fallback.
	costTx func(ctx context.Context, tx any, log aicost.Log) error
}

// NewMySQLStore wraps an opened database handle.
func NewMySQLStore(db *sql.DB) *MySQLStore {
	return &MySQLStore{db: db}
}

// SetCostTx wires the function used to insert cost ledger rows inside
// MarkSessionReviewedWithCost's transaction. It must be called before any
// review write — typically by session.OpenStore in MySQL mode.
func (s *MySQLStore) SetCostTx(costTx func(ctx context.Context, tx any, log aicost.Log) error) {
	s.costTx = costTx
}

// Ping verifies database connectivity.
func (s *MySQLStore) Ping(ctx context.Context) error {
	return s.db.PingContext(ctx)
}

const sessionColumns = `id, user_id, material_id, scene_type, status, duration_sec, review_json, created_at, updated_at, deleted_at`

// CreateSession inserts a practice session row.
func (s *MySQLStore) CreateSession(ctx context.Context, session Session) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO practice_sessions (
			id, user_id, material_id, scene_type, status, duration_sec, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, session.ID, session.UserID, nullString(session.MaterialID), session.SceneType, session.Status,
		session.DurationSec, session.CreatedAt, session.UpdatedAt)
	return err
}

// GetSession returns a session by id.
func (s *MySQLStore) GetSession(ctx context.Context, id string) (Session, error) {
	return scanSession(s.db.QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM practice_sessions WHERE id = ?`, id))
}

// ListSessions returns a user's non-deleted sessions newest-first (created_at DESC, id DESC).
func (s *MySQLStore) ListSessions(ctx context.Context, userID string, lastStartedAt *time.Time, lastID string, limit int) ([]Session, error) {
	if limit < 1 {
		return nil, nil
	}
	query := `SELECT ` + sessionColumns + ` FROM practice_sessions WHERE user_id = ? AND deleted_at IS NULL`
	args := []any{userID}
	if lastStartedAt != nil {
		query += ` AND (created_at < ? OR (created_at = ? AND id < ?))`
		at := lastStartedAt.UTC()
		args = append(args, at, at, lastID)
	}
	query += ` ORDER BY created_at DESC, id DESC LIMIT ?`
	args = append(args, limit)
	rows, err := s.db.QueryContext(ctx, query, args...)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	out := make([]Session, 0, limit)
	for rows.Next() {
		session, err := scanSession(rows)
		if err != nil {
			return nil, err
		}
		out = append(out, session)
	}
	return out, rows.Err()
}

// ListActiveUserIDs returns distinct non-deleted users with a session since since.
func (s *MySQLStore) ListActiveUserIDs(ctx context.Context, since time.Time) ([]string, error) {
	rows, err := s.db.QueryContext(ctx, `
		SELECT DISTINCT user_id FROM practice_sessions
		WHERE created_at >= ? AND deleted_at IS NULL
		ORDER BY user_id
	`, since.UTC())
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()
	var out []string
	for rows.Next() {
		var id string
		if err := rows.Scan(&id); err != nil {
			return nil, err
		}
		out = append(out, id)
	}
	return out, rows.Err()
}

// CreateTicket inserts a one-time WSS ticket row.
func (s *MySQLStore) CreateTicket(ctx context.Context, ticket Ticket) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO session_tickets (
			id, session_id, user_id, token_hash, expires_at, used_at, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)
	`, ticket.ID, ticket.SessionID, ticket.UserID, ticket.Hash, ticket.ExpiresAt, nullTime(ticket.UsedAt), ticket.CreatedAt)
	return err
}

// CreateSessionWithTicket inserts a session and ticket in one transaction.
func (s *MySQLStore) CreateSessionWithTicket(ctx context.Context, session Session, ticket Ticket) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	if _, err := tx.ExecContext(ctx, `
		INSERT INTO practice_sessions (
			id, user_id, material_id, scene_type, status, duration_sec, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?)
	`, session.ID, session.UserID, nullString(session.MaterialID), session.SceneType, session.Status,
		session.DurationSec, session.CreatedAt, session.UpdatedAt); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		INSERT INTO session_tickets (
			id, session_id, user_id, token_hash, expires_at, used_at, created_at
		) VALUES (?, ?, ?, ?, ?, ?, ?)
	`, ticket.ID, ticket.SessionID, ticket.UserID, ticket.Hash, ticket.ExpiresAt, nullTime(ticket.UsedAt), ticket.CreatedAt); err != nil {
		return err
	}
	return tx.Commit()
}

// GetTicketByHash returns a ticket by hashed raw value.
func (s *MySQLStore) GetTicketByHash(ctx context.Context, hash string) (Ticket, error) {
	row := s.db.QueryRowContext(ctx, `
		SELECT id, session_id, user_id, token_hash, expires_at, used_at, created_at
		FROM session_tickets
		WHERE token_hash = ?
	`, hash)
	return scanTicket(row)
}

// ConsumeTicket atomically marks an unused, unexpired ticket as used.
func (s *MySQLStore) ConsumeTicket(ctx context.Context, hash string, at time.Time) (Ticket, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Ticket{}, err
	}
	defer func() { _ = tx.Rollback() }()

	row := tx.QueryRowContext(ctx, `
		SELECT id, session_id, user_id, token_hash, expires_at, used_at, created_at
		FROM session_tickets
		WHERE token_hash = ?
		FOR UPDATE
	`, hash)
	ticket, err := scanTicket(row)
	if err != nil {
		return Ticket{}, err
	}
	if ticket.UsedAt != nil {
		return ticket, ErrTicketUsed
	}
	if !ticket.ExpiresAt.After(at) {
		return ticket, ErrTicketExpired
	}
	result, err := tx.ExecContext(ctx, `
		UPDATE session_tickets
		SET used_at = ?
		WHERE token_hash = ? AND used_at IS NULL AND expires_at > ?
	`, at, hash, at)
	if err != nil {
		return Ticket{}, err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return Ticket{}, err
	}
	if n != 1 {
		return Ticket{}, ErrTicketUsed
	}
	usedAt := at
	ticket.UsedAt = &usedAt
	if err := tx.Commit(); err != nil {
		return Ticket{}, err
	}
	return ticket, nil
}

// MarkSessionActive transitions created → active (idempotent if already active).
func (s *MySQLStore) MarkSessionActive(ctx context.Context, sessionID string, at time.Time) (Session, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Session{}, err
	}
	defer func() { _ = tx.Rollback() }()

	session, err := scanSession(tx.QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM practice_sessions WHERE id = ? FOR UPDATE`, sessionID))
	if err != nil {
		return Session{}, err
	}
	switch session.Status {
	case StatusActive:
		if err := tx.Commit(); err != nil {
			return Session{}, err
		}
		return session, nil
	case StatusCreated:
		if _, err := tx.ExecContext(ctx, `
			UPDATE practice_sessions SET status = ?, updated_at = ? WHERE id = ?
		`, StatusActive, at, sessionID); err != nil {
			return Session{}, err
		}
		session.Status = StatusActive
		session.UpdatedAt = at
		if err := tx.Commit(); err != nil {
			return Session{}, err
		}
		return session, nil
	default:
		return session, ErrConflict
	}
}

// EndSession marks a session ended and writes utterances in one transaction.
func (s *MySQLStore) EndSession(ctx context.Context, sessionID string, durationSec int, utterances []Utterance, at time.Time, costLog *aicost.Log) (Session, []Utterance, bool, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Session{}, nil, false, err
	}
	defer func() { _ = tx.Rollback() }()

	session, err := scanSession(tx.QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM practice_sessions WHERE id = ? FOR UPDATE`, sessionID))
	if err != nil {
		return Session{}, nil, false, err
	}
	if session.Status == StatusEnded {
		existing, listErr := listUtterancesTx(ctx, tx, sessionID)
		if listErr != nil {
			return Session{}, nil, false, listErr
		}
		if err := tx.Commit(); err != nil {
			return Session{}, nil, false, err
		}
		return session, existing, true, nil
	}
	if session.Status != StatusCreated && session.Status != StatusActive {
		return session, nil, false, ErrConflict
	}
	if durationSec < 0 {
		durationSec = 0
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE practice_sessions
		SET status = ?, duration_sec = ?, updated_at = ?
		WHERE id = ?
	`, StatusEnded, durationSec, at, sessionID); err != nil {
		return Session{}, nil, false, err
	}
	if _, err := tx.ExecContext(ctx, `DELETE FROM utterances WHERE session_id = ?`, sessionID); err != nil {
		return Session{}, nil, false, err
	}
	for _, u := range utterances {
		if _, err := tx.ExecContext(ctx, `
			INSERT INTO utterances (
				id, session_id, seq, speaker, text, asr_confidence, audio_url, created_at, interrupted
			) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?)
		`, u.ID, sessionID, u.Seq, u.Speaker, u.Text, nullFloat(u.ASRConfidence), nullString(u.AudioURL), u.CreatedAt, u.Interrupted); err != nil {
			return Session{}, nil, false, err
		}
	}
	session.Status = StatusEnded
	session.DurationSec = durationSec
	session.UpdatedAt = at
	saved := make([]Utterance, 0, len(utterances))
	for _, u := range utterances {
		u.SessionID = sessionID
		saved = append(saved, cloneUtterance(u))
	}
	// Same transaction as the session and its transcript: a session must not be
	// able to end with its usage billed but its transcript missing, or the
	// reverse.
	if costLog != nil {
		// Same rule as MarkSessionReviewedWithCost: without the transactional
		// writer there is no way to keep "session ended" and "usage billed" in
		// one commit, so refuse rather than write the session and quietly lose
		// the row.
		if s.costTx == nil {
			return Session{}, nil, false, errors.New("end session: cost ledger writer is not wired")
		}
		if err := s.costTx(ctx, tx, *costLog); err != nil {
			return Session{}, nil, false, err
		}
	}
	if err := tx.Commit(); err != nil {
		return Session{}, nil, false, err
	}
	return session, saved, false, nil
}

// ListUtterances returns transcript rows ordered by seq.
func (s *MySQLStore) ListUtterances(ctx context.Context, sessionID string) ([]Utterance, error) {
	if _, err := s.GetSession(ctx, sessionID); err != nil {
		return nil, err
	}
	return listUtterancesTx(ctx, s.db, sessionID)
}

// EnqueueJob inserts a pending outbox job.
func (s *MySQLStore) EnqueueJob(ctx context.Context, job Job) error {
	_, err := s.db.ExecContext(ctx, `
		INSERT INTO session_jobs (
			id, session_id, job_type, status, attempts, available_at, locked_at, locked_by, last_error, created_at, updated_at
		) VALUES (?, ?, ?, ?, ?, ?, ?, ?, ?, ?, ?)
	`, job.ID, job.SessionID, job.JobType, job.Status, job.Attempts, job.AvailableAt, nullTime(job.LockedAt), nullString(job.LockedBy), nullString(job.LastError), job.CreatedAt, job.UpdatedAt)
	return err
}

// HasSessionJob reports whether a job exists for session+type in any given status.
func (s *MySQLStore) HasSessionJob(ctx context.Context, sessionID, jobType string, statuses ...string) (bool, error) {
	if len(statuses) == 0 {
		return false, nil
	}
	placeholders := make([]string, len(statuses))
	args := make([]any, 0, 2+len(statuses))
	args = append(args, sessionID, jobType)
	for i, st := range statuses {
		placeholders[i] = "?"
		args = append(args, st)
	}
	var one int
	err := s.db.QueryRowContext(ctx, `
		SELECT 1 FROM session_jobs
		WHERE session_id = ? AND job_type = ? AND status IN (`+strings.Join(placeholders, ",")+`)
		LIMIT 1
	`, args...).Scan(&one)
	if errors.Is(err, sql.ErrNoRows) {
		return false, nil
	}
	if err != nil {
		return false, err
	}
	return true, nil
}

// ClaimNextJob claims one available pending job, or a stale processing lease.
func (s *MySQLStore) ClaimNextJob(ctx context.Context, workerID string, at time.Time) (Job, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Job{}, err
	}
	defer func() { _ = tx.Rollback() }()

	leaseCutoff := at.Add(-DefaultJobLease)
	row := tx.QueryRowContext(ctx, `
		SELECT id, session_id, job_type, status, attempts, available_at, locked_at, locked_by, last_error, created_at, updated_at
		FROM session_jobs
		WHERE (status = ? AND available_at <= ?)
		   OR (status = ? AND locked_at IS NOT NULL AND locked_at <= ?)
		ORDER BY created_at ASC, id ASC
		LIMIT 1
		FOR UPDATE
	`, JobStatusPending, at, JobStatusProcessing, leaseCutoff)
	job, err := scanJob(row)
	if err != nil {
		return Job{}, err
	}
	job.Status = JobStatusProcessing
	job.Attempts++
	lockedAt := at
	lockedBy := workerID
	job.LockedAt = &lockedAt
	job.LockedBy = &lockedBy
	job.UpdatedAt = at
	if _, err := tx.ExecContext(ctx, `
		UPDATE session_jobs
		SET status = ?, attempts = ?, locked_at = ?, locked_by = ?, updated_at = ?
		WHERE id = ?
	`, job.Status, job.Attempts, lockedAt, lockedBy, at, job.ID); err != nil {
		return Job{}, err
	}
	if err := tx.Commit(); err != nil {
		return Job{}, err
	}
	return job, nil
}

// CompleteJob marks a claimed job done.
func (s *MySQLStore) CompleteJob(ctx context.Context, jobID string, at time.Time) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE session_jobs
		SET status = ?, locked_at = NULL, locked_by = NULL, updated_at = ?
		WHERE id = ? AND status = ?
	`, JobStatusDone, at, jobID, JobStatusProcessing)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n == 0 {
		return jobTransitionConflict(ctx, s.db, jobID)
	}
	return nil
}

// FailJob retries once or marks the job failed permanently.
func (s *MySQLStore) FailJob(ctx context.Context, jobID string, at time.Time, errMsg string, retryDelay time.Duration) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	job, err := scanJob(tx.QueryRowContext(ctx, `
		SELECT id, session_id, job_type, status, attempts, available_at, locked_at, locked_by, last_error, created_at, updated_at
		FROM session_jobs WHERE id = ? FOR UPDATE
	`, jobID))
	if err != nil {
		return err
	}
	if job.Status != JobStatusProcessing {
		return ErrConflict
	}
	status := JobStatusFailed
	availableAt := at
	if job.Attempts < MaxJobAttempts {
		status = JobStatusPending
		availableAt = at.Add(retryDelay)
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE session_jobs
		SET status = ?, available_at = ?, locked_at = NULL, locked_by = NULL, last_error = ?, updated_at = ?
		WHERE id = ? AND status = ?
	`, status, availableAt, errMsg, at, jobID, JobStatusProcessing); err != nil {
		return err
	}
	return tx.Commit()
}

// MarkSessionReviewed writes review_json and status=reviewed.
func (s *MySQLStore) MarkSessionReviewed(ctx context.Context, sessionID string, reviewJSON []byte, at time.Time) (Session, error) {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Session{}, err
	}
	defer func() { _ = tx.Rollback() }()

	session, err := scanSession(tx.QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM practice_sessions WHERE id = ? FOR UPDATE`, sessionID))
	if err != nil {
		return Session{}, err
	}
	switch session.Status {
	case StatusReviewed:
		if err := tx.Commit(); err != nil {
			return Session{}, err
		}
		return session, nil
	case StatusEnded:
		if _, err := tx.ExecContext(ctx, `
			UPDATE practice_sessions
			SET status = ?, review_json = ?, updated_at = ?
			WHERE id = ?
		`, StatusReviewed, reviewJSON, at, sessionID); err != nil {
			return Session{}, err
		}
		session.Status = StatusReviewed
		session.ReviewJSON = append([]byte(nil), reviewJSON...)
		session.UpdatedAt = at
		if err := tx.Commit(); err != nil {
			return Session{}, err
		}
		return session, nil
	default:
		return session, ErrConflict
	}
}

// MarkSessionReviewedWithCost writes review_json + status=reviewed and inserts one
// ai_cost_logs row in a single database transaction. The two writes commit together
// so a cost ledger row never exists without its review (acceptance criterion:
// "成本写入失败视为任务失败", and "不允许后补"). When session.status is already
// StatusReviewed (idempotent retry) we commit without the cost insert to avoid
// double-billing on retry storms.
//
// The cost INSERT itself is delegated to s.costTx (wired by session.OpenStore to
// aicost.MySQLStore.RecordCostTx) so the cost ledger SQL lives in exactly one
// place — the aicost package.
func (s *MySQLStore) MarkSessionReviewedWithCost(ctx context.Context, sessionID string, reviewJSON []byte, at time.Time, costLog aicost.Log) (Session, error) {
	if s.costTx == nil {
		return Session{}, errors.New("session: MySQLStore.costTx not wired; call SetCostTx before MarkSessionReviewedWithCost")
	}
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return Session{}, err
	}
	defer func() { _ = tx.Rollback() }()

	session, err := scanSession(tx.QueryRowContext(ctx, `SELECT `+sessionColumns+` FROM practice_sessions WHERE id = ? FOR UPDATE`, sessionID))
	if err != nil {
		return Session{}, err
	}
	switch session.Status {
	case StatusReviewed:
		// Idempotent retry: session is already reviewed. Do not double-bill the
		// cost ledger; just commit and return the existing session.
		if err := tx.Commit(); err != nil {
			return Session{}, err
		}
		return session, nil
	case StatusEnded:
		if _, err := tx.ExecContext(ctx, `
			UPDATE practice_sessions
			SET status = ?, review_json = ?, updated_at = ?
			WHERE id = ?
		`, StatusReviewed, reviewJSON, at, sessionID); err != nil {
			return Session{}, err
		}
		// Same transaction: cost ledger row. Either both commit or both rollback.
		if err := s.costTx(ctx, tx, costLog); err != nil {
			return Session{}, fmt.Errorf("insert ai_cost_logs: %w", err)
		}
		session.Status = StatusReviewed
		session.ReviewJSON = append([]byte(nil), reviewJSON...)
		session.UpdatedAt = at
		if err := tx.Commit(); err != nil {
			return Session{}, err
		}
		return session, nil
	default:
		return session, ErrConflict
	}
}

type queryRower interface {
	QueryContext(ctx context.Context, query string, args ...any) (*sql.Rows, error)
}

func listUtterancesTx(ctx context.Context, q queryRower, sessionID string) ([]Utterance, error) {
	rows, err := q.QueryContext(ctx, `
		SELECT id, session_id, seq, speaker, text, asr_confidence, audio_url, llm_eval_json, created_at, interrupted
		FROM utterances
		WHERE session_id = ?
		ORDER BY seq ASC
	`, sessionID)
	if err != nil {
		return nil, err
	}
	defer func() { _ = rows.Close() }()

	var out []Utterance
	for rows.Next() {
		var u Utterance
		var confidence sql.NullFloat64
		var audioURL sql.NullString
		var evalJSON []byte
		if err := rows.Scan(&u.ID, &u.SessionID, &u.Seq, &u.Speaker, &u.Text, &confidence, &audioURL, &evalJSON, &u.CreatedAt, &u.Interrupted); err != nil {
			return nil, err
		}
		if confidence.Valid {
			v := confidence.Float64
			u.ASRConfidence = &v
		}
		if audioURL.Valid {
			v := audioURL.String
			u.AudioURL = &v
		}
		if len(evalJSON) > 0 {
			u.LLMEvalJSON = evalJSON
		}
		out = append(out, u)
	}
	return out, rows.Err()
}

// ReassignUser moves sessions and tickets from a guest onto a registered account.
func (s *MySQLStore) ReassignUser(ctx context.Context, fromUserID, toUserID string) error {
	tx, err := s.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}
	defer func() { _ = tx.Rollback() }()

	now := time.Now().UTC()
	if _, err := tx.ExecContext(ctx, `
		UPDATE practice_sessions SET user_id = ?, updated_at = ? WHERE user_id = ?
	`, toUserID, now, fromUserID); err != nil {
		return err
	}
	if _, err := tx.ExecContext(ctx, `
		UPDATE session_tickets SET user_id = ? WHERE user_id = ?
	`, toUserID, fromUserID); err != nil {
		return err
	}
	return tx.Commit()
}

// SoftDeleteForUser marks practice_sessions.deleted_at without changing scanSession.
func (s *MySQLStore) SoftDeleteForUser(ctx context.Context, userID string, deletedAt time.Time) (int, error) {
	result, err := s.db.ExecContext(ctx, `
		UPDATE practice_sessions
		SET deleted_at = ?, updated_at = ?
		WHERE user_id = ? AND deleted_at IS NULL
	`, deletedAt.UTC(), deletedAt.UTC(), userID)
	if err != nil {
		return 0, err
	}
	n, err := result.RowsAffected()
	return int(n), err
}

// RestoreDeletedForUser clears A4 soft-delete on practice_sessions.
func (s *MySQLStore) RestoreDeletedForUser(ctx context.Context, userID string) (int, error) {
	result, err := s.db.ExecContext(ctx, `
		UPDATE practice_sessions
		SET deleted_at = NULL
		WHERE user_id = ? AND deleted_at IS NOT NULL
	`, userID)
	if err != nil {
		return 0, err
	}
	n, err := result.RowsAffected()
	return int(n), err
}

// SaveUtteranceEval writes utterances.llm_eval_json for B18.
func (s *MySQLStore) SaveUtteranceEval(ctx context.Context, utteranceID string, evalJSON []byte) error {
	result, err := s.db.ExecContext(ctx, `
		UPDATE utterances SET llm_eval_json = ? WHERE id = ?
	`, evalJSON, utteranceID)
	if err != nil {
		return err
	}
	n, err := result.RowsAffected()
	if err != nil {
		return err
	}
	if n != 1 {
		return ErrNotFound
	}
	return nil
}

type sessionRow interface {
	Scan(dest ...any) error
}

func scanSession(row sessionRow) (Session, error) {
	var session Session
	var materialID sql.NullString
	var reviewJSON []byte
	var deletedAt sql.NullTime
	err := row.Scan(
		&session.ID,
		&session.UserID,
		&materialID,
		&session.SceneType,
		&session.Status,
		&session.DurationSec,
		&reviewJSON,
		&session.CreatedAt,
		&session.UpdatedAt,
		&deletedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Session{}, ErrNotFound
	}
	if err != nil {
		return Session{}, err
	}
	if materialID.Valid {
		value := materialID.String
		session.MaterialID = &value
	}
	if len(reviewJSON) > 0 {
		session.ReviewJSON = append([]byte(nil), reviewJSON...)
	}
	if deletedAt.Valid {
		t := deletedAt.Time.UTC()
		session.DeletedAt = &t
	}
	return session, nil
}

func jobTransitionConflict(ctx context.Context, db *sql.DB, jobID string) error {
	var status string
	err := db.QueryRowContext(ctx, `SELECT status FROM session_jobs WHERE id = ?`, jobID).Scan(&status)
	if errors.Is(err, sql.ErrNoRows) {
		return ErrNotFound
	}
	if err != nil {
		return err
	}
	return ErrConflict
}

func scanJob(row *sql.Row) (Job, error) {
	var job Job
	var lockedAt sql.NullTime
	var lockedBy sql.NullString
	var lastError sql.NullString
	err := row.Scan(
		&job.ID,
		&job.SessionID,
		&job.JobType,
		&job.Status,
		&job.Attempts,
		&job.AvailableAt,
		&lockedAt,
		&lockedBy,
		&lastError,
		&job.CreatedAt,
		&job.UpdatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Job{}, ErrNotFound
	}
	if err != nil {
		return Job{}, err
	}
	if lockedAt.Valid {
		v := lockedAt.Time
		job.LockedAt = &v
	}
	if lockedBy.Valid {
		v := lockedBy.String
		job.LockedBy = &v
	}
	if lastError.Valid {
		v := lastError.String
		job.LastError = &v
	}
	return job, nil
}

func scanTicket(row *sql.Row) (Ticket, error) {
	var ticket Ticket
	var usedAt sql.NullTime
	err := row.Scan(
		&ticket.ID,
		&ticket.SessionID,
		&ticket.UserID,
		&ticket.Hash,
		&ticket.ExpiresAt,
		&usedAt,
		&ticket.CreatedAt,
	)
	if errors.Is(err, sql.ErrNoRows) {
		return Ticket{}, ErrNotFound
	}
	if err != nil {
		return Ticket{}, err
	}
	if usedAt.Valid {
		value := usedAt.Time
		ticket.UsedAt = &value
	}
	return ticket, nil
}

func nullString(value *string) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullTime(value *time.Time) any {
	if value == nil {
		return nil
	}
	return *value
}

func nullFloat(value *float64) any {
	if value == nil {
		return nil
	}
	return *value
}
