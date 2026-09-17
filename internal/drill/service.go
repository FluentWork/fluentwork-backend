package drill

import (
	"context"
	"errors"
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
	cfg     Config
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
		cfg:     DefaultConfig(),
	}
}

// SetConfig replaces the E3 scheduling knobs. Wiring passes the same
// corpus.Schedule it gave the corpus store, so a hit and a drill answer can
// never promote a block on different terms.
func (s *Service) SetConfig(cfg Config) {
	if s == nil {
		return
	}
	s.cfg = cfg.Normalize()
}

// Round returns due cards for the user.
func (s *Service) Round(ctx context.Context, userID string, size int) (Round, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return Round{}, apierr.Unauthenticated("missing authenticated user")
	}
	if size <= 0 {
		size = s.cfg.Normalize().RoundSize
	}
	if size > MaxRoundSize {
		size = MaxRoundSize
	}
	now := s.now().UTC()
	blocks, err := SelectBlocksForRound(ctx, s.blocks, userID, now, size, s.roundOptions(ctx, userID, now))
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

// roundOptions decides whether this round may release 灰 blocks.
//
// The count comes from the ledger rather than a counter we keep: every attempt
// records the state its block was in (drill_records.prev_state), so "how many
// new blocks has this user already been given today" is a fact already stored,
// and it survives restarts without a second source of truth.
//
// A ledger read failure logs and releases the blocks: a bookkeeping outage must
// not be able to empty a user's drill.
func (s *Service) roundOptions(ctx context.Context, userID string, now time.Time) RoundOptions {
	cfg := s.cfg.Normalize()
	if cfg.DailyNewBlockLimit <= 0 {
		return DefaultRoundOptions()
	}
	dayStart := time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, time.UTC)
	served, err := s.records.CountNewReleasesSince(ctx, userID, dayStart)
	if err != nil {
		s.logger.Warn("new block budget unavailable; releasing without cap",
			"user_id", userID, "err", err)
		return DefaultRoundOptions()
	}
	if served >= cfg.DailyNewBlockLimit {
		return RoundOptions{IncludeNew: false}
	}
	return DefaultRoundOptions()
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
	updated := s.cfg.Normalize().Schedule.ApplyJudge(block, result.Pass, now)
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
		// E2 snapshot: the schedule as it stood before this attempt, so an
		// appeal can restore it exactly instead of re-deriving it (PRD §7.5).
		PrevState:         block.State,
		PrevSuccessStreak: block.SuccessStreak,
		PrevNextDueAt:     block.NextDueAt,
		CreatedAt:         now,
	}
	recordID, err := s.records.Insert(ctx, rec)
	if err != nil {
		return JudgeResponse{}, err
	}
	return JudgeResponse{
		Pass:          result.Pass,
		JudgeReason:   result.Reason,
		SuccessStreak: saved.SuccessStreak,
		State:         saved.State,
		NextDueAt:     saved.NextDueAt.UTC().Format(time.RFC3339Nano),
		Recorded:      true,
		RecordID:      recordID,
		ASRText:       asr,
	}, nil
}

// Appeal implements E2's 一键申诉 (PRD §7.5, V1.7 定案).
//
// Two things are kept apart on purpose:
//
//   - **The round's settlement stands.** The record keeps whatever the judge
//     said, because a spoken appeal is not evidence — treating it as fact would
//     turn the button into a self-comfort button.
//   - **The block is not charged for a failure it may not have earned.** The
//     schedule returns to its pre-attempt snapshot: the streak is not zeroed,
//     the state does not fall back, and next_due_at goes back to its original
//     time — "未做判定", waiting for the next normal review.
//
// The restore applies only to an attempt the judge failed, only where the
// snapshot exists, and only while no newer attempt sits on that block; each of
// those refusals answers with a note instead of silently doing nothing.
func (s *Service) Appeal(ctx context.Context, userID string, req AppealRequest) (AppealResponse, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return AppealResponse{}, apierr.Unauthenticated("missing authenticated user")
	}
	if req.RecordID <= 0 {
		return AppealResponse{}, apierr.InvalidArgument("record_id is required")
	}
	rec, err := s.records.GetRecord(ctx, userID, req.RecordID)
	if err != nil {
		if errors.Is(err, ErrRecordNotFound) {
			return AppealResponse{}, apierr.NotFound("drill record not found")
		}
		return AppealResponse{}, err
	}

	resp := AppealResponse{RecordID: rec.ID, BlockID: rec.BlockID}
	if rec.AppealedAt != nil {
		// Repeat appeal: nothing changes, and the answer still tells the
		// client where the block stands.
		resp.AlreadyAppealed = true
		resp.Note = "already appealed"
		s.fillAppealBlockState(ctx, userID, &resp)
		return resp, nil
	}

	now := s.now().UTC()
	switch {
	case rec.SemanticPass:
		resp.Note = "attempt passed; nothing to restore"
	case rec.PrevState == "":
		resp.Note = "no schedule snapshot on this attempt"
	default:
		latest, err := s.records.IsLatestForBlock(ctx, userID, rec.BlockID, rec.ID)
		if err != nil {
			return AppealResponse{}, err
		}
		switch {
		case !latest:
			// A newer attempt already moved this block; rolling back to an
			// older snapshot would erase it.
			resp.Note = "a newer attempt exists on this block"
		default:
			saved, err := s.blocks.UpdateSchedule(ctx, userID, rec.BlockID,
				rec.PrevState, rec.PrevSuccessStreak, rec.PrevNextDueAt, now)
			if err != nil {
				if errors.Is(err, corpus.ErrNotFound) {
					return AppealResponse{}, apierr.NotFound("phrase block not found")
				}
				return AppealResponse{}, err
			}
			resp.Restored = true
			resp.State = saved.State
			resp.SuccessStreak = saved.SuccessStreak
			resp.NextDueAt = saved.NextDueAt.UTC().Format(time.RFC3339Nano)
		}
	}

	first, err := s.records.MarkAppealed(ctx, userID, rec.ID, now)
	if err != nil {
		if errors.Is(err, ErrRecordNotFound) {
			return AppealResponse{}, apierr.NotFound("drill record not found")
		}
		return AppealResponse{}, err
	}
	if !first {
		// A parallel appeal stamped it between our read and our write.
		resp.AlreadyAppealed = true
	}
	if first {
		incAppeal()
	}
	if resp.State == "" {
		s.fillAppealBlockState(ctx, userID, &resp)
	}
	s.logger.Info("drill appeal recorded",
		"block_id", rec.BlockID,
		"record_id", rec.ID,
		"user_id", userID,
		"restored", resp.Restored,
		"already_appealed", resp.AlreadyAppealed,
		"note", resp.Note,
	)
	return resp, nil
}

// fillAppealBlockState reports where the block stands now, for the appeal
// answers that changed nothing.
func (s *Service) fillAppealBlockState(ctx context.Context, userID string, resp *AppealResponse) {
	block, err := s.blocks.PeekBlock(ctx, resp.BlockID)
	if err != nil || block.UserID != userID || block.DeletedAt != nil {
		return
	}
	resp.State = block.State
	resp.SuccessStreak = block.SuccessStreak
	resp.NextDueAt = block.NextDueAt.UTC().Format(time.RFC3339Nano)
}

// WipeRecords hard-deletes drill_records for A4.
func (s *Service) WipeRecords(ctx context.Context, userID string) (int, error) {
	if s == nil || s.records == nil {
		return 0, nil
	}
	n, err := s.records.DeleteForUser(ctx, userID)
	return n, err
}
