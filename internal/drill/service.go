package drill

import (
	"context"
	"log/slog"
	"strings"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/apierr"
	"github.com/FluentWork/fluentwork-backend/internal/corpus"
)

// Service runs drill rounds and judges.
type Service struct {
	blocks  corpus.Store
	records RecordStore
	judge   *LLMJudge
	logger  *slog.Logger
	now     func() time.Time
}

// NewService constructs a drill service.
func NewService(blocks corpus.Store, records RecordStore, judge *LLMJudge, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	if records == nil {
		records = NewMemoryRecordStore()
	}
	return &Service{
		blocks:  blocks,
		records: records,
		judge:   judge,
		logger:  logger.With("component", "drill.service"),
		now:     time.Now,
	}
}

// Round returns due cards for the user.
func (s *Service) Round(ctx context.Context, userID string, size int) (Round, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return Round{}, apierr.Unauthenticated("missing authenticated user")
	}
	if size <= 0 {
		size = DefaultRoundSize
	}
	if size > MaxRoundSize {
		size = MaxRoundSize
	}
	blocks, err := SelectBlocksForRound(ctx, s.blocks, userID, s.now().UTC(), size)
	if err != nil {
		return Round{}, err
	}
	cards := make([]Card, 0, len(blocks))
	for _, block := range blocks {
		cards = append(cards, Card{
			BlockID:      block.ID,
			IntentZH:     block.IntentZH,
			ExpressionEN: block.ExpressionEN,
			State:        block.State,
		})
	}
	return Round{Size: len(cards), Cards: cards}, nil
}

// Judge scores one attempt, writes drill_records, and updates SM-2 state.
func (s *Service) Judge(ctx context.Context, userID string, req JudgeRequest) (JudgeResponse, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return JudgeResponse{}, apierr.Unauthenticated("missing authenticated user")
	}
	blockID := strings.TrimSpace(req.BlockID)
	if blockID == "" {
		return JudgeResponse{}, apierr.InvalidArgument("block_id is required")
	}
	asr := strings.TrimSpace(req.ASRText)
	if asr == "" {
		return JudgeResponse{}, apierr.InvalidArgument("asr_text is required")
	}
	if len(asr) > 500 {
		asr = asr[:500]
	}
	block, err := s.blocks.PeekBlock(ctx, blockID)
	if err != nil {
		if err == corpus.ErrNotFound {
			return JudgeResponse{}, apierr.NotFound("block not found")
		}
		return JudgeResponse{}, err
	}
	if block.DeletedAt != nil {
		return JudgeResponse{}, apierr.NotFound("block not found")
	}
	if block.UserID != userID {
		return JudgeResponse{}, apierr.PermissionDenied("block belongs to another user")
	}
	result, err := s.judge.Judge(ctx, block.ExpressionEN, asr)
	if err != nil {
		return JudgeResponse{}, err
	}
	now := s.now().UTC()
	updated := ApplyJudge(block, result.Pass, now)
	saved, err := s.blocks.UpdateSchedule(ctx, userID, block.ID, updated.State, updated.SuccessStreak, updated.NextDueAt, now)
	if err != nil {
		if err == corpus.ErrNotFound {
			return JudgeResponse{}, apierr.NotFound("block not found")
		}
		return JudgeResponse{}, err
	}
	incStateTransition(block.State, saved.State)
	rec := Record{
		UserID:       userID,
		BlockID:      block.ID,
		SessionID:    strings.TrimSpace(req.SessionID),
		DrillType:    DrillTypeRecall,
		SemanticPass: result.Pass,
		ResponseMS:   req.ResponseMS,
		ASRText:      asr,
		JudgeReason:  result.Reason,
		CreatedAt:    now,
	}
	if err := s.records.Insert(ctx, rec); err != nil {
		return JudgeResponse{}, err
	}
	return JudgeResponse{
		Pass:          result.Pass,
		JudgeReason:   result.Reason,
		SuccessStreak: saved.SuccessStreak,
		State:         saved.State,
		NextDueAt:     saved.NextDueAt.UTC().Format(time.RFC3339Nano),
		Recorded:      true,
	}, nil
}

// WipeRecords hard-deletes drill_records for A4.
func (s *Service) WipeRecords(ctx context.Context, userID string) (int, error) {
	if s == nil || s.records == nil {
		return 0, nil
	}
	n, err := s.records.DeleteForUser(ctx, userID)
	return n, err
}
