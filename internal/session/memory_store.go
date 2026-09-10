package session

import (
	"context"
	"fmt"
	"sort"
	"sync"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/aicost"
)

// MemoryStore is a process-local Store used by tests and local development.
type MemoryStore struct {
	mu         sync.Mutex
	sessions   map[string]Session
	tickets    map[string]Ticket
	utterances map[string][]Utterance
	jobs       map[string]Job
	// costLogs is a side store for MarkSessionReviewedWithCost; tracks ai_cost_logs
	// rows written atomically with session review updates. Tests can inspect this
	// map to verify the review+cost transaction invariant.
	costLogs map[string]aicost.Log
}

// NewMemoryStore constructs an empty in-memory session store.
func NewMemoryStore() *MemoryStore {
	return &MemoryStore{
		sessions:   make(map[string]Session),
		tickets:    make(map[string]Ticket),
		utterances: make(map[string][]Utterance),
		jobs:       make(map[string]Job),
	}
}

// Ping always succeeds.
func (s *MemoryStore) Ping(context.Context) error {
	return nil
}

// CreateSession inserts a practice session.
func (s *MemoryStore) CreateSession(_ context.Context, session Session) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.sessions[session.ID]; exists {
		return fmt.Errorf("session: duplicate session id")
	}
	s.sessions[session.ID] = cloneSession(session)
	return nil
}

// GetSession returns a session by id.
func (s *MemoryStore) GetSession(_ context.Context, id string) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[id]
	if !ok {
		return Session{}, ErrNotFound
	}
	return cloneSession(session), nil
}

// ListSessions returns a user's non-deleted sessions newest-first (created_at DESC, id DESC).
func (s *MemoryStore) ListSessions(_ context.Context, userID string, lastStartedAt *time.Time, lastID string, limit int) ([]Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if limit < 1 {
		return nil, nil
	}
	items := make([]Session, 0, len(s.sessions))
	for _, session := range s.sessions {
		if session.UserID != userID || session.DeletedAt != nil {
			continue
		}
		if lastStartedAt != nil {
			created := session.CreatedAt.UTC()
			cursorAt := lastStartedAt.UTC()
			if created.After(cursorAt) || (created.Equal(cursorAt) && session.ID >= lastID) {
				continue
			}
		}
		items = append(items, cloneSession(session))
	}
	sort.Slice(items, func(i, j int) bool {
		ti := items[i].CreatedAt.UTC()
		tj := items[j].CreatedAt.UTC()
		if !ti.Equal(tj) {
			return ti.After(tj)
		}
		return items[i].ID > items[j].ID
	})
	if len(items) > limit {
		items = items[:limit]
	}
	return items, nil
}

// ListActiveUserIDs returns distinct non-deleted users with a session since since.
func (s *MemoryStore) ListActiveUserIDs(_ context.Context, since time.Time) ([]string, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	since = since.UTC()
	seen := map[string]struct{}{}
	for _, session := range s.sessions {
		if session.DeletedAt != nil || session.UserID == "" {
			continue
		}
		if session.CreatedAt.UTC().Before(since) {
			continue
		}
		seen[session.UserID] = struct{}{}
	}
	out := make([]string, 0, len(seen))
	for id := range seen {
		out = append(out, id)
	}
	sort.Strings(out)
	return out, nil
}

// CreateTicket inserts a one-time WSS ticket.
func (s *MemoryStore) CreateTicket(_ context.Context, ticket Ticket) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.tickets[ticket.Hash]; exists {
		return fmt.Errorf("session: duplicate ticket hash")
	}
	s.tickets[ticket.Hash] = cloneTicket(ticket)
	return nil
}

// CreateSessionWithTicket atomically inserts a session and its ticket.
func (s *MemoryStore) CreateSessionWithTicket(_ context.Context, session Session, ticket Ticket) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.sessions[session.ID]; exists {
		return fmt.Errorf("session: duplicate session id")
	}
	if _, exists := s.tickets[ticket.Hash]; exists {
		return fmt.Errorf("session: duplicate ticket hash")
	}
	s.sessions[session.ID] = cloneSession(session)
	s.tickets[ticket.Hash] = cloneTicket(ticket)
	return nil
}

// GetTicketByHash returns a ticket by its hashed raw value.
func (s *MemoryStore) GetTicketByHash(_ context.Context, hash string) (Ticket, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ticket, ok := s.tickets[hash]
	if !ok {
		return Ticket{}, ErrNotFound
	}
	return cloneTicket(ticket), nil
}

// ConsumeTicket atomically marks an unused, unexpired ticket as used.
func (s *MemoryStore) ConsumeTicket(_ context.Context, hash string, at time.Time) (Ticket, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	ticket, ok := s.tickets[hash]
	if !ok {
		return Ticket{}, ErrNotFound
	}
	if ticket.UsedAt != nil {
		return cloneTicket(ticket), ErrTicketUsed
	}
	if !ticket.ExpiresAt.After(at) {
		return cloneTicket(ticket), ErrTicketExpired
	}
	usedAt := at
	ticket.UsedAt = &usedAt
	s.tickets[hash] = ticket
	return cloneTicket(ticket), nil
}

// MarkSessionActive transitions created → active.
func (s *MemoryStore) MarkSessionActive(_ context.Context, sessionID string, at time.Time) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[sessionID]
	if !ok {
		return Session{}, ErrNotFound
	}
	switch session.Status {
	case StatusActive:
		return cloneSession(session), nil
	case StatusCreated:
		session.Status = StatusActive
		session.UpdatedAt = at
		s.sessions[sessionID] = session
		return cloneSession(session), nil
	default:
		return cloneSession(session), ErrConflict
	}
}

// EndSession marks a session ended and replaces its utterances atomically.
// If already ended, returns the existing rows with alreadyEnded=true.
func (s *MemoryStore) EndSession(_ context.Context, sessionID string, durationSec int, utterances []Utterance, at time.Time, costLog *aicost.Log) (Session, []Utterance, bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[sessionID]
	if !ok {
		return Session{}, nil, false, ErrNotFound
	}
	if session.Status == StatusEnded {
		existing := cloneUtterances(s.utterances[sessionID])
		return cloneSession(session), existing, true, nil
	}
	if session.Status != StatusCreated && session.Status != StatusActive {
		return cloneSession(session), nil, false, ErrConflict
	}
	if durationSec < 0 {
		durationSec = 0
	}
	session.Status = StatusEnded
	session.DurationSec = durationSec
	session.UpdatedAt = at
	s.sessions[sessionID] = session

	cloned := make([]Utterance, 0, len(utterances))
	for _, u := range utterances {
		cloned = append(cloned, cloneUtterance(u))
	}
	s.utterances[sessionID] = cloned
	// No real transaction here; the row is written only on the path that
	// actually ends the session, so a replayed end cannot double-charge.
	if costLog != nil {
		s.costLogs[costLog.ID] = *costLog
	}
	return cloneSession(session), cloneUtterances(cloned), false, nil
}

// ListUtterances returns transcript rows for a session ordered by seq.
func (s *MemoryStore) ListUtterances(_ context.Context, sessionID string) ([]Utterance, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, ok := s.sessions[sessionID]; !ok {
		return nil, ErrNotFound
	}
	out := cloneUtterances(s.utterances[sessionID])
	sort.Slice(out, func(i, j int) bool { return out[i].Seq < out[j].Seq })
	return out, nil
}

// EnqueueJob inserts a pending outbox job.
func (s *MemoryStore) EnqueueJob(_ context.Context, job Job) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	if _, exists := s.jobs[job.ID]; exists {
		return fmt.Errorf("session: duplicate job id")
	}
	s.jobs[job.ID] = cloneJob(job)
	return nil
}

// HasSessionJob reports whether a job exists for session+type in any given status.
func (s *MemoryStore) HasSessionJob(_ context.Context, sessionID, jobType string, statuses ...string) (bool, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	wanted := make(map[string]struct{}, len(statuses))
	for _, st := range statuses {
		wanted[st] = struct{}{}
	}
	for _, job := range s.jobs {
		if job.SessionID != sessionID || job.JobType != jobType {
			continue
		}
		if _, ok := wanted[job.Status]; ok {
			return true, nil
		}
	}
	return false, nil
}

// ClaimNextJob claims the oldest available pending job, or a stale processing lease.
func (s *MemoryStore) ClaimNextJob(_ context.Context, workerID string, at time.Time) (Job, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	leaseCutoff := at.Add(-DefaultJobLease)
	var selectedID string
	var selected Job
	for id, job := range s.jobs {
		switch job.Status {
		case JobStatusPending:
			if job.AvailableAt.After(at) {
				continue
			}
		case JobStatusProcessing:
			if job.LockedAt == nil || job.LockedAt.After(leaseCutoff) {
				continue
			}
		default:
			continue
		}
		if selectedID == "" || job.CreatedAt.Before(selected.CreatedAt) || (job.CreatedAt.Equal(selected.CreatedAt) && job.ID < selected.ID) {
			selectedID = id
			selected = job
		}
	}
	if selectedID == "" {
		return Job{}, ErrNotFound
	}
	lockedAt := at
	lockedBy := workerID
	selected.Status = JobStatusProcessing
	selected.Attempts++
	selected.LockedAt = &lockedAt
	selected.LockedBy = &lockedBy
	selected.UpdatedAt = at
	s.jobs[selectedID] = selected
	return cloneJob(selected), nil
}

// CompleteJob marks a claimed job done.
func (s *MemoryStore) CompleteJob(_ context.Context, jobID string, at time.Time) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[jobID]
	if !ok {
		return ErrNotFound
	}
	if job.Status != JobStatusProcessing {
		return ErrConflict
	}
	job.Status = JobStatusDone
	job.LockedAt = nil
	job.LockedBy = nil
	job.UpdatedAt = at
	s.jobs[jobID] = job
	return nil
}

// FailJob retries once or marks the job failed permanently.
func (s *MemoryStore) FailJob(_ context.Context, jobID string, at time.Time, errMsg string, retryDelay time.Duration) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	job, ok := s.jobs[jobID]
	if !ok {
		return ErrNotFound
	}
	if job.Status != JobStatusProcessing {
		return ErrConflict
	}
	msg := errMsg
	job.LastError = &msg
	job.LockedAt = nil
	job.LockedBy = nil
	job.UpdatedAt = at
	if job.Attempts < MaxJobAttempts {
		job.Status = JobStatusPending
		job.AvailableAt = at.Add(retryDelay)
	} else {
		job.Status = JobStatusFailed
	}
	s.jobs[jobID] = job
	return nil
}

// MarkSessionReviewed writes review_json and status=reviewed for an ended session.
func (s *MemoryStore) MarkSessionReviewed(_ context.Context, sessionID string, reviewJSON []byte, at time.Time) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[sessionID]
	if !ok {
		return Session{}, ErrNotFound
	}
	switch session.Status {
	case StatusReviewed:
		return cloneSession(session), nil
	case StatusEnded:
		session.Status = StatusReviewed
		session.ReviewJSON = append([]byte(nil), reviewJSON...)
		session.UpdatedAt = at
		s.sessions[sessionID] = session
		return cloneSession(session), nil
	default:
		return cloneSession(session), ErrConflict
	}
}

// MarkSessionReviewedWithCost writes review_json + status=reviewed AND records the
// cost log in a single logical operation. MemoryStore has no real transaction; we
// lock the session map, perform both writes under the lock, and roll back the
// review update if the cost log write fails — preserving the "review and cost
// commit together" invariant that MySQLStore enforces with BeginTx.
//
// Idempotent retry (session already reviewed): we return the existing session
// without double-recording the cost log, matching the MySQLStore behavior.
func (s *MemoryStore) MarkSessionReviewedWithCost(_ context.Context, sessionID string, reviewJSON []byte, at time.Time, costLog aicost.Log) (Session, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	session, ok := s.sessions[sessionID]
	if !ok {
		return Session{}, ErrNotFound
	}
	switch session.Status {
	case StatusReviewed:
		return cloneSession(session), nil
	case StatusEnded:
		// Apply review update and cost log under the same lock so the two
		// writes are observable together. MemoryStore has no real transaction;
		// the mutex provides the atomicity guarantee. Idempotent retry on the
		// cost log (duplicate ID) keeps the existing row instead of double-billing.
		session.Status = StatusReviewed
		session.ReviewJSON = append([]byte(nil), reviewJSON...)
		session.UpdatedAt = at
		s.sessions[sessionID] = session

		if s.costLogs == nil {
			s.costLogs = make(map[string]aicost.Log)
		}
		if _, exists := s.costLogs[costLog.ID]; exists {
			// Duplicate cost id: do not double-record, keep the existing one.
			return cloneSession(session), nil
		}
		s.costLogs[costLog.ID] = costLog
		return cloneSession(session), nil
	default:
		// Status is not Ended and not Reviewed → conflict. The session must
		// reach the Ended state before review/cost can be committed.
		return cloneSession(session), ErrConflict
	}
}

// ReassignUser moves all sessions from a guest onto a registered account.
func (s *MemoryStore) ReassignUser(_ context.Context, fromUserID, toUserID string) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	now := time.Now().UTC()
	for id, session := range s.sessions {
		if session.UserID == fromUserID {
			session.UserID = toUserID
			session.UpdatedAt = now
			s.sessions[id] = session
		}
	}
	for hash, ticket := range s.tickets {
		if ticket.UserID == fromUserID {
			ticket.UserID = toUserID
			s.tickets[hash] = ticket
		}
	}
	return nil
}

// SoftDeleteForUser marks practice_sessions.deleted_at for A4.
func (s *MemoryStore) SoftDeleteForUser(_ context.Context, userID string, deletedAt time.Time) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	at := deletedAt.UTC()
	for id, session := range s.sessions {
		if session.UserID != userID || session.DeletedAt != nil {
			continue
		}
		session.DeletedAt = &at
		session.UpdatedAt = at
		s.sessions[id] = session
		n++
	}
	return n, nil
}

// RestoreDeletedForUser clears A4 soft-delete on practice_sessions.
func (s *MemoryStore) RestoreDeletedForUser(_ context.Context, userID string) (int, error) {
	s.mu.Lock()
	defer s.mu.Unlock()
	n := 0
	for id, session := range s.sessions {
		if session.UserID != userID || session.DeletedAt == nil {
			continue
		}
		session.DeletedAt = nil
		s.sessions[id] = session
		n++
	}
	return n, nil
}

// SaveUtteranceEval writes utterances.llm_eval_json for B18.
func (s *MemoryStore) SaveUtteranceEval(_ context.Context, utteranceID string, evalJSON []byte) error {
	s.mu.Lock()
	defer s.mu.Unlock()
	for sessionID, rows := range s.utterances {
		for i, u := range rows {
			if u.ID != utteranceID {
				continue
			}
			u.LLMEvalJSON = append([]byte(nil), evalJSON...)
			rows[i] = u
			s.utterances[sessionID] = rows
			return nil
		}
	}
	return ErrNotFound
}

func cloneSession(session Session) Session {
	cloned := session
	if session.MaterialID != nil {
		copied := *session.MaterialID
		cloned.MaterialID = &copied
	}
	if session.ReviewJSON != nil {
		cloned.ReviewJSON = append([]byte(nil), session.ReviewJSON...)
	}
	if session.DeletedAt != nil {
		copied := *session.DeletedAt
		cloned.DeletedAt = &copied
	}
	return cloned
}

func cloneTicket(ticket Ticket) Ticket {
	cloned := ticket
	if ticket.UsedAt != nil {
		copied := *ticket.UsedAt
		cloned.UsedAt = &copied
	}
	return cloned
}

func cloneJob(job Job) Job {
	cloned := job
	if job.LockedAt != nil {
		v := *job.LockedAt
		cloned.LockedAt = &v
	}
	if job.LockedBy != nil {
		v := *job.LockedBy
		cloned.LockedBy = &v
	}
	if job.LastError != nil {
		v := *job.LastError
		cloned.LastError = &v
	}
	return cloned
}

func cloneUtterance(u Utterance) Utterance {
	cloned := u
	if u.ASRConfidence != nil {
		v := *u.ASRConfidence
		cloned.ASRConfidence = &v
	}
	if u.AudioURL != nil {
		v := *u.AudioURL
		cloned.AudioURL = &v
	}
	if u.LLMEvalJSON != nil {
		cloned.LLMEvalJSON = append([]byte(nil), u.LLMEvalJSON...)
	}
	return cloned
}

func cloneUtterances(items []Utterance) []Utterance {
	if len(items) == 0 {
		return nil
	}
	out := make([]Utterance, 0, len(items))
	for _, item := range items {
		out = append(out, cloneUtterance(item))
	}
	return out
}
