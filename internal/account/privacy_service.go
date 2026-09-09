package account

import (
	"context"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/FluentWork/fluentwork-backend/internal/apierr"
)

// DataWiper soft-deletes or anonymizes one entity type and can restore it.
type DataWiper interface {
	EntityType() string
	Wipe(ctx context.Context, userID string, at time.Time) (int, error)
	Restore(ctx context.Context, userID string) (int, error)
}

// HardDeleter permanently deletes rows that A4 does not restore.
type HardDeleter interface {
	EntityType() string
	Delete(ctx context.Context, userID string) (int, error)
}

// PrivacyService runs A4 delete, export enqueue, and support undelete.
type PrivacyService struct {
	users    Store
	wipers   []DataWiper
	hard     []HardDeleter
	logger   *slog.Logger
	now      func() time.Time
	newID    func() string
	exportMu sync.Mutex
	exports  map[string]ExportJob
}

// NewPrivacyService constructs A4 privacy operations.
func NewPrivacyService(users Store, wipers []DataWiper, hard []HardDeleter, logger *slog.Logger) *PrivacyService {
	if logger == nil {
		logger = slog.Default()
	}
	return &PrivacyService{
		users:   users,
		wipers:  wipers,
		hard:    hard,
		logger:  logger.With("component", "account.privacy"),
		now:     time.Now,
		newID:   uuid.NewString,
		exports: make(map[string]ExportJob),
	}
}

// SetClock overrides the clock for tests.
func (s *PrivacyService) SetClock(now func() time.Time) {
	if s != nil && now != nil {
		s.now = now
	}
}

// DeleteAllData applies the A4 cascade. Wrong confirmation is 422.
// A second call with the same confirmation is idempotent.
func (s *PrivacyService) DeleteAllData(ctx context.Context, userID, confirmationCode string) (DeleteDataResult, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return DeleteDataResult{}, apierr.Unauthenticated("missing authenticated user")
	}
	if strings.TrimSpace(confirmationCode) != ConfirmationDeleteMyData {
		return DeleteDataResult{}, apierr.FailedPrecondition("confirmation_code must be DELETE-MY-DATA")
	}
	user, err := s.users.GetUser(ctx, userID)
	if err != nil {
		if err == ErrNotFound {
			return DeleteDataResult{}, apierr.NotFound("user not found")
		}
		return DeleteDataResult{}, err
	}
	now := s.now().UTC()
	purgeAt := now.Add(UndeleteWindow)
	if user.DeletedAt != nil {
		backup := purgeAt.Format(time.RFC3339Nano)
		if user.TombstoneAt != nil {
			backup = user.TombstoneAt.UTC().Add(UndeleteWindow).Format(time.RFC3339Nano)
		}
		incPrivacyDelete()
		return DeleteDataResult{Cascaded: map[string]int{}, BackupPurgeAt: backup, AlreadyDeleted: true}, nil
	}

	cascaded := make(map[string]int)
	wiped := make([]DataWiper, 0, len(s.wipers))
	for _, wiper := range s.wipers {
		if wiper == nil {
			continue
		}
		n, err := wiper.Wipe(ctx, userID, now)
		if err != nil {
			s.restoreWipers(ctx, userID, wiped)
			return DeleteDataResult{}, err
		}
		cascaded[wiper.EntityType()] = n
		wiped = append(wiped, wiper)
	}
	for _, del := range s.hard {
		if del == nil {
			continue
		}
		n, err := del.Delete(ctx, userID)
		if err != nil {
			s.restoreWipers(ctx, userID, wiped)
			return DeleteDataResult{}, err
		}
		cascaded[del.EntityType()] = n
	}
	if err := s.users.MarkDeleted(ctx, userID, now, now); err != nil {
		s.restoreWipers(ctx, userID, wiped)
		return DeleteDataResult{}, err
	}
	_ = s.users.DeleteRefreshTokensForUser(ctx, userID)
	cascaded["users"] = 1
	for entity, n := range cascaded {
		if n <= 0 && entity != "users" {
			continue
		}
		row := Tombstone{
			ID:         s.newID(),
			UserID:     userID,
			EntityType: entity,
			EntityID:   userID,
			DeletedAt:  now,
			PurgeAt:    purgeAt,
			CreatedAt:  now,
		}
		if err := s.users.InsertTombstone(ctx, row); err != nil {
			s.logger.Error("insert tombstone", "entity", entity, "err", err)
			continue
		}
		incTombstone(entity)
	}
	incPrivacyDelete()
	return DeleteDataResult{
		Cascaded:      cascaded,
		BackupPurgeAt: purgeAt.Format(time.RFC3339Nano),
	}, nil
}

func (s *PrivacyService) restoreWipers(ctx context.Context, userID string, wiped []DataWiper) {
	for i := len(wiped) - 1; i >= 0; i-- {
		if _, err := wiped[i].Restore(ctx, userID); err != nil && s.logger != nil {
			s.logger.Error("privacy compensate restore", "entity", wiped[i].EntityType(), "err", err)
		}
	}
}

// EnqueueExport records an export job stub. Email send is V1.5.
func (s *PrivacyService) EnqueueExport(ctx context.Context, userID, emailTo string) (ExportDataResult, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return ExportDataResult{}, apierr.Unauthenticated("missing authenticated user")
	}
	user, err := s.users.GetUser(ctx, userID)
	if err != nil {
		if err == ErrNotFound {
			return ExportDataResult{}, apierr.NotFound("user not found")
		}
		return ExportDataResult{}, err
	}
	if user.DeletedAt != nil {
		return ExportDataResult{}, apierr.FailedPrecondition("account is deleted")
	}
	emailTo = strings.TrimSpace(emailTo)
	if emailTo == "" && user.Email != nil {
		emailTo = strings.TrimSpace(*user.Email)
	}
	now := s.now().UTC()
	job := ExportJob{
		ID:               s.newID(),
		UserID:           userID,
		EmailTo:          emailTo,
		EstimatedReadyAt: now.Add(ExportReadyDelay),
		CreatedAt:        now,
	}
	s.exportMu.Lock()
	s.exports[job.ID] = job
	s.exportMu.Unlock()
	return ExportDataResult{
		ExportID:         job.ID,
		EmailTo:          job.EmailTo,
		EstimatedReadyAt: job.EstimatedReadyAt.Format(time.RFC3339Nano),
	}, nil
}

// UndeleteUser restores a user inside the 30-day window.
func (s *PrivacyService) UndeleteUser(ctx context.Context, userID, actor, reason string) (UndeleteUserResult, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return UndeleteUserResult{}, apierr.InvalidArgument("user_id is required")
	}
	user, err := s.users.GetUser(ctx, userID)
	if err != nil {
		if err == ErrNotFound {
			return UndeleteUserResult{}, apierr.NotFound("user not found")
		}
		return UndeleteUserResult{}, err
	}
	if user.DeletedAt == nil {
		return UndeleteUserResult{}, apierr.FailedPrecondition("account is not deleted")
	}
	now := s.now().UTC()
	anchor := user.DeletedAt.UTC()
	if user.TombstoneAt != nil {
		anchor = user.TombstoneAt.UTC()
	}
	if now.After(anchor.Add(UndeleteWindow)) {
		return UndeleteUserResult{}, apierr.FailedPrecondition("undelete window has expired")
	}
	restored := make(map[string]int)
	for _, wiper := range s.wipers {
		if wiper == nil {
			continue
		}
		n, err := wiper.Restore(ctx, userID)
		if err != nil {
			return UndeleteUserResult{}, err
		}
		restored[wiper.EntityType()] = n
	}
	if err := s.users.ClearDeleted(ctx, userID); err != nil {
		return UndeleteUserResult{}, err
	}
	restored["users"] = 1
	if _, err := s.users.DeleteTombstonesForUser(ctx, userID); err != nil {
		return UndeleteUserResult{}, err
	}
	actor = strings.TrimSpace(actor)
	if actor == "" {
		actor = "support"
	}
	if err := s.users.InsertAudit(ctx, AuditLog{
		ID:        s.newID(),
		Actor:     actor,
		Action:    "undelete-user",
		UserID:    userID,
		Reason:    strings.TrimSpace(reason),
		CreatedAt: now,
	}); err != nil {
		return UndeleteUserResult{}, err
	}
	incPrivacyUndelete()
	return UndeleteUserResult{Restored: restored, UserID: userID}, nil
}

// ExportJob returns a previously enqueued stub for tests.
func (s *PrivacyService) ExportJob(id string) (ExportJob, bool) {
	s.exportMu.Lock()
	defer s.exportMu.Unlock()
	job, ok := s.exports[id]
	return job, ok
}
