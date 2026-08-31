package content

import (
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"time"

	"github.com/google/uuid"

	"github.com/FluentWork/fluentwork-backend/internal/apierr"
)

// Service implements daily read business rules.
type Service struct {
	store       Store
	blockSource BlockSource
	logger      *slog.Logger
	now         func() time.Time
	newID       func() string
	genMu       sync.Mutex
	genLocks    map[string]*sync.Mutex
}

// NewService constructs the content service.
func NewService(store Store, blockSource BlockSource, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		store:       store,
		blockSource: blockSource,
		logger:      logger.With("component", "content.service"),
		now:         time.Now,
		newID:       uuid.NewString,
		genLocks:    make(map[string]*sync.Mutex),
	}
}

// Reassigner adapts Store to account guest merge.
type Reassigner struct {
	Store Store
}

// ReassignFromGuest moves guest-owned daily reads to the registered user.
func (r Reassigner) ReassignFromGuest(ctx context.Context, guestUserID, targetUserID string) error {
	if r.Store == nil {
		return nil
	}
	return r.Store.ReassignUser(ctx, guestUserID, targetUserID)
}

// GetToday returns today's daily read, generating synchronously on first request.
func (s *Service) GetToday(ctx context.Context, userID string) (TodayPollResponse, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return TodayPollResponse{}, apierr.Unauthenticated("missing authenticated user")
	}
	today := normalizeDateUTC(s.now())
	read, err := s.ensureTodayRead(ctx, userID, today)
	if err != nil {
		return TodayPollResponse{}, err
	}
	return toPollResponse(read), nil
}

func (s *Service) ensureTodayRead(ctx context.Context, userID string, today time.Time) (DailyRead, error) {
	lock := s.generationLock(userID, today)
	lock.Lock()
	defer lock.Unlock()

	read, err := s.store.GetByUserDate(ctx, userID, today)
	if err == nil && read.Status == StatusReady {
		return read, nil
	}
	if err != nil && !errors.Is(err, ErrNotFound) {
		return DailyRead{}, err
	}

	now := s.now().UTC()
	if errors.Is(err, ErrNotFound) {
		read = DailyRead{
			ID:        s.newID(),
			UserID:    userID,
			GenDate:   today,
			Status:    StatusPending,
			CreatedAt: now,
			UpdatedAt: now,
		}
		read, err = s.store.CreatePending(ctx, read)
		if err != nil {
			return DailyRead{}, err
		}
	}

	if read.Status == StatusReady {
		return read, nil
	}

	ready, err := s.generateAndPersist(ctx, read)
	if err != nil {
		s.logger.Warn("daily read generation failed; applying fallback",
			"user_id", userID,
			"gen_date", today.Format("2006-01-02"),
			"err", err,
		)
		return s.store.MarkReady(ctx, applyGenerated(read, presetDailyRead(), s.now().UTC()))
	}
	return ready, nil
}

// FollowRead records a follow-read submission without scoring.
func (s *Service) FollowRead(ctx context.Context, userID, readID string, req FollowReadRequest) (FollowReadResponse, error) {
	userID = strings.TrimSpace(userID)
	readID = strings.TrimSpace(readID)
	if userID == "" {
		return FollowReadResponse{}, apierr.Unauthenticated("missing authenticated user")
	}
	read, err := s.store.GetBlock(ctx, userID, readID)
	if err != nil {
		if errors.Is(err, ErrNotFound) {
			return FollowReadResponse{}, apierr.NotFound("daily read not found")
		}
		return FollowReadResponse{}, err
	}
	if read.Status != StatusReady {
		return FollowReadResponse{}, apierr.Conflict("daily read is not ready")
	}
	_ = req
	return FollowReadResponse{
		DailyReadID: read.ID,
		Recorded:    true,
		Score:       nil,
		Generator:   GeneratorFollowRead,
	}, nil
}

// RunScheduledGeneration is the 02:00 batch skeleton for active users.
func (s *Service) RunScheduledGeneration(_ context.Context, date time.Time) error {
	s.logger.Info("daily read batch skeleton",
		"gen_date", normalizeDateUTC(date).Format("2006-01-02"),
		"stage", "scheduler",
	)
	return nil
}

func (s *Service) generateAndPersist(ctx context.Context, pending DailyRead) (DailyRead, error) {
	blocks, err := s.listBlocks(ctx, pending.UserID)
	if err != nil {
		return DailyRead{}, err
	}
	generated := GenerateDailyRead(blocks)
	return s.store.MarkReady(ctx, applyGenerated(pending, generated, s.now().UTC()))
}

func (s *Service) listBlocks(ctx context.Context, userID string) ([]SourceBlock, error) {
	if s.blockSource == nil {
		return nil, nil
	}
	return s.blockSource.ListRecentBlocks(ctx, userID, maxBlocksInDailyRead)
}

func applyGenerated(pending DailyRead, generated GeneratedDailyRead, updatedAt time.Time) DailyRead {
	out := pending
	out.Status = StatusReady
	out.Title = generated.Title
	out.Body = generated.Body
	out.Generator = generated.Generator
	out.UsedBlockIDs = append(json.RawMessage(nil), generated.UsedBlockIDs...)
	out.SourceRefs = append(json.RawMessage(nil), generated.SourceRefs...)
	out.UpdatedAt = updatedAt.UTC()
	return out
}

func toPollResponse(read DailyRead) TodayPollResponse {
	resp := TodayPollResponse{
		GenDate: normalizeDateUTC(read.GenDate).Format("2006-01-02"),
		Status:  read.Status,
	}
	if read.Status == StatusReady {
		resp.DailyRead = &DailyReadView{
			ID:           read.ID,
			Title:        read.Title,
			Body:         read.Body,
			AudioURL:     read.AudioURL,
			Generator:    read.Generator,
			UsedBlockIDs: read.UsedBlockIDs,
			SourceRefs:   read.SourceRefs,
			ReadScore:    read.ReadScore,
		}
	}
	return resp
}

func (s *Service) generationLock(userID string, date time.Time) *sync.Mutex {
	key := userDateKey(userID, date)
	s.genMu.Lock()
	defer s.genMu.Unlock()
	lock, ok := s.genLocks[key]
	if !ok {
		lock = &sync.Mutex{}
		s.genLocks[key] = lock
	}
	return lock
}
