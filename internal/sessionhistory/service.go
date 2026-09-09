package sessionhistory

import (
	"context"
	"errors"
	"log/slog"
	"strings"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/apierr"
	"github.com/FluentWork/fluentwork-backend/internal/session"
)

// ReviewSummarizer is the B18 eval summary hook.
type ReviewSummarizer interface {
	Summary(ctx context.Context, sessionID string) (*session.EvalSummary, error)
}

// Service lists practice sessions and returns detail for history (B24).
type Service struct {
	store  session.Store
	eval   ReviewSummarizer
	logger *slog.Logger
}

// NewService constructs a history service.
func NewService(store session.Store, eval ReviewSummarizer, logger *slog.Logger) *Service {
	if logger == nil {
		logger = slog.Default()
	}
	return &Service{store: store, eval: eval, logger: logger.With("component", "sessionhistory")}
}

// List returns a cursor page of the caller's non-deleted sessions.
func (s *Service) List(ctx context.Context, userID, cursor string, size int) (SessionListPage, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return SessionListPage{}, apierr.Unauthenticated("missing authenticated user")
	}
	if size < 1 || size > MaxPageSize {
		size = DefaultPageSize
	}

	var lastStarted *time.Time
	lastID := ""
	if strings.TrimSpace(cursor) != "" {
		decoded, err := decodeCursor(cursor)
		if err != nil {
			return SessionListPage{}, apierr.InvalidArgument("invalid cursor")
		}
		t := decoded.StartedAt.UTC()
		lastStarted = &t
		lastID = decoded.ID
	}

	rows, err := s.store.ListSessions(ctx, userID, lastStarted, lastID, size+1)
	if err != nil {
		return SessionListPage{}, err
	}

	page := SessionListPage{Items: make([]SessionListItem, 0, size), Size: size}
	for i, row := range rows {
		if i == size {
			token, err := encodeCursor(Cursor{StartedAt: rows[size-1].CreatedAt.UTC(), ID: rows[size-1].ID})
			if err != nil {
				return SessionListPage{}, err
			}
			page.NextCursor = &token
			break
		}
		page.Items = append(page.Items, toListItem(row))
	}
	return page, nil
}

// GetDetail returns one session owned by userID, including utterances and B18 review.
func (s *Service) GetDetail(ctx context.Context, userID, sessionID string) (SessionDetail, error) {
	userID = strings.TrimSpace(userID)
	sessionID = strings.TrimSpace(sessionID)
	if userID == "" {
		return SessionDetail{}, apierr.Unauthenticated("missing authenticated user")
	}
	if sessionID == "" {
		return SessionDetail{}, apierr.InvalidArgument("session_id is required")
	}

	sess, err := s.store.GetSession(ctx, sessionID)
	if err != nil {
		if errors.Is(err, session.ErrNotFound) {
			return SessionDetail{}, apierr.NotFound("session not found")
		}
		return SessionDetail{}, err
	}
	if sess.UserID != userID {
		return SessionDetail{}, apierr.PermissionDenied("session not owned by caller")
	}
	if sess.DeletedAt != nil {
		return SessionDetail{}, apierr.NotFound("session not found")
	}

	utts, err := s.store.ListUtterances(ctx, sessionID)
	if err != nil {
		if errors.Is(err, session.ErrNotFound) {
			utts = nil
		} else {
			return SessionDetail{}, err
		}
	}

	detail := SessionDetail{
		SessionID:   sess.ID,
		SceneType:   sess.SceneType,
		Status:      sess.Status,
		StartedAt:   sess.CreatedAt.UTC(),
		DurationSec: sess.DurationSec,
		MaterialID:  sess.MaterialID,
		Materials:   []MaterialRef{},
		Utterances:  toUtteranceViews(utts),
	}
	review, err := s.detailReview(ctx, sess)
	if err != nil {
		s.logger.Warn("session history review", "session_id", sessionID, "err", err)
	} else {
		detail.Review = review
	}
	return detail, nil
}

func (s *Service) detailReview(ctx context.Context, sess session.Session) (*DetailReview, error) {
	failed, err := s.store.HasSessionJob(ctx, sess.ID, session.JobTypeSessionFinished, session.JobStatusFailed)
	if err != nil {
		return nil, err
	}
	if failed {
		return &DetailReview{Status: session.ReviewPollFailed, Score: 0}, nil
	}
	if sess.Status != session.StatusReviewed || s.eval == nil {
		return nil, nil
	}
	summary, err := s.eval.Summary(ctx, sess.ID)
	if err != nil {
		return nil, err
	}
	if summary == nil || !summary.Complete {
		return nil, nil
	}
	dims := Dims{
		Grammar:    summary.Dims.Grammar,
		Fluency:    summary.Dims.Fluency,
		Vocabulary: summary.Dims.Vocabulary,
	}
	suggestions := summary.Suggestions
	if suggestions == nil {
		suggestions = []string{}
	}
	return &DetailReview{
		Status:      session.ReviewPollReady,
		Score:       summary.Score,
		Dims:        &dims,
		Suggestions: suggestions,
	}, nil
}

func toListItem(sess session.Session) SessionListItem {
	return SessionListItem{
		SessionID:   sess.ID,
		SceneType:   sess.SceneType,
		Status:      sess.Status,
		StartedAt:   sess.CreatedAt.UTC(),
		DurationSec: sess.DurationSec,
		MaterialID:  sess.MaterialID,
	}
}

func toUtteranceViews(utts []session.Utterance) []UtteranceView {
	out := make([]UtteranceView, 0, len(utts))
	for _, u := range utts {
		out = append(out, UtteranceView{Seq: u.Seq, Speaker: u.Speaker, Text: u.Text})
	}
	return out
}
