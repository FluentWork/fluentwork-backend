package aicost

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/google/uuid"

	"github.com/FluentWork/fluentwork-backend/internal/apierr"
)

// Service validates and writes ai_cost_logs ledger rows.
type Service struct {
	store  Store
	logger *slog.Logger
	now    func() time.Time
	newID  func() string
}

// NewService constructs an aicost service.
func NewService(store Store, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{
		store:  store,
		logger: logger.With("component", "aicost.service"),
		now:    time.Now,
		newID:  uuid.NewString,
	}
}

// Record validates a ledger entry and persists it synchronously.
func (s *Service) Record(ctx context.Context, req RecordRequest) (Log, error) {
	if s.store == nil {
		return Log{}, apierr.Internal("aicost store is not configured")
	}
	taskType := strings.TrimSpace(req.TaskType)
	if taskType == "" {
		return Log{}, apierr.InvalidArgument("task_type is required")
	}
	model := strings.TrimSpace(req.Model)
	if model == "" {
		return Log{}, apierr.InvalidArgument("model is required")
	}
	if req.TokensIn < 0 || req.TokensOut < 0 || req.AudioSec < 0 || req.CostFen < 0 {
		return Log{}, apierr.InvalidArgument("tokens/audio_sec/cost_fen must be non-negative")
	}

	log := Log{
		ID:        s.newID(),
		TaskType:  taskType,
		Model:     model,
		TokensIn:  req.TokensIn,
		TokensOut: req.TokensOut,
		AudioSec:  req.AudioSec,
		CostFen:   req.CostFen,
		CreatedAt: s.now().UTC(),
	}
	if userID := strings.TrimSpace(req.UserID); userID != "" {
		log.UserID = &userID
	}

	if err := s.store.CreateLog(ctx, log); err != nil {
		return Log{}, err
	}
	s.logger.Info("ai cost recorded",
		"log_id", log.ID,
		"user_id", log.UserID,
		"task_type", log.TaskType,
		"model", log.Model,
		"tokens_in", log.TokensIn,
		"tokens_out", log.TokensOut,
		"audio_sec", log.AudioSec,
		"cost_fen", log.CostFen,
	)
	return log, nil
}

// ListRecent returns recent rows for one user or all users when userID is empty.
func (s *Service) ListRecent(ctx context.Context, userID string, limit int) ([]Log, error) {
	if s.store == nil {
		return nil, apierr.Internal("aicost store is not configured")
	}
	return s.store.ListRecent(ctx, userID, limit)
}
