package drill

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/apierr"
	"github.com/FluentWork/fluentwork-backend/internal/corpus"
)

// RescueSource reports B8 rescue activity for one learner. Optional: without it
// the stuck map still reports the drill half.
//
// It is an interface rather than a store because rescues live in the session
// module: the map spans both, and neither module should own the other's tables.
type RescueSource interface {
	// CountRescuesByPath returns how many ladders fired per path
	// ("incomplete" / "silent") in the window.
	CountRescuesByPath(ctx context.Context, userID string, since time.Time) (map[string]int, error)
}

// StuckRow is one function tag's stuck profile (86_ M4).
//
// The unit is the *intent type*, not the phrase: "which kinds of things does
// this learner keep failing to say" is the question content work can act on.
type StuckRow struct {
	FunctionTag string `json:"function_tag"`
	Blocks      int    `json:"blocks"`
	Attempts    int    `json:"attempts"`
	Failures    int    `json:"failures"`
	// ForgotAfterLearning counts failures on a block this learner had already
	// recalled at least once. That is 86_ M2's distinction: a block forgotten
	// after learning is a spacing problem, a block failed from the start is a
	// difficulty or material problem, and the two want different fixes.
	ForgotAfterLearning int `json:"forgot_after_learning"`
	Promotions          int `json:"promotions"`
	// AvgAttemptsPerPromotion is attempts/promotions over the window: how much
	// work this intent type costs per phrase brought to 绿.
	AvgAttemptsPerPromotion float64 `json:"avg_attempts_per_promotion"`
	LastFailureAt           string  `json:"last_failure_at,omitempty"`
}

// StuckMapResponse is GET /internal/v1/drill/stuck-map.
type StuckMapResponse struct {
	WindowDays int        `json:"window_days"`
	Rows       []StuckRow `json:"rows"`
	// Rescues counts B8 ladders by path over the same window.
	Rescues     map[string]int `json:"rescues,omitempty"`
	GeneratedAt string         `json:"generated_at"`
	// Note says what this is for, because an internal endpoint with no stated
	// purpose gets read as a dashboard.
	Note string `json:"note"`
}

const (
	// DefaultStuckWindowDays is the reporting window when the caller names none.
	DefaultStuckWindowDays = 30
	// MaxStuckWindowDays bounds the scan.
	MaxStuckWindowDays = 180
)

// StuckMap aggregates where this learner's recall keeps breaking.
//
// It reads the ledger rather than the counters: the counters know a block's
// present state, not the path it took, and the path is the interesting part.
func (s *Service) StuckMap(ctx context.Context, userID string, days int) (StuckMapResponse, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return StuckMapResponse{}, apierr.Unauthenticated("missing authenticated user")
	}
	if days <= 0 {
		days = DefaultStuckWindowDays
	}
	if days > MaxStuckWindowDays {
		days = MaxStuckWindowDays
	}
	now := s.now().UTC()
	since := now.AddDate(0, 0, -days)

	records, err := s.records.ListRecordsSince(ctx, userID, since)
	if err != nil {
		return StuckMapResponse{}, err
	}
	functionByBlock := s.functionTags(ctx, userID)

	rows := map[string]*StuckRow{}
	blockSeen := map[string]map[string]struct{}{}
	for _, rec := range records {
		tag := functionByBlock[rec.BlockID]
		if tag == "" {
			// A block the learner has since deleted still tells us about the
			// intent type it belonged to only if we can still see it; without a
			// tag the row would be noise.
			tag = "unknown"
		}
		row, ok := rows[tag]
		if !ok {
			row = &StuckRow{FunctionTag: tag}
			rows[tag] = row
			blockSeen[tag] = map[string]struct{}{}
		}
		blockSeen[tag][rec.BlockID] = struct{}{}
		if !rec.Judged {
			continue // an unjudged attempt says nothing about recall
		}
		row.Attempts++
		if rec.SemanticPass {
			if rec.PrevSuccessStreak == 0 && rec.PrevState != corpus.StateAutomated {
				row.Promotions++ // first success on a block that was never recalled
			}
			continue
		}
		row.Failures++
		if rec.PrevSuccessStreak > 0 {
			row.ForgotAfterLearning++
		}
		if at := rec.CreatedAt.UTC().Format(time.RFC3339); at > row.LastFailureAt {
			row.LastFailureAt = at
		}
	}

	out := make([]StuckRow, 0, len(rows))
	for tag, row := range rows {
		row.Blocks = len(blockSeen[tag])
		if row.Promotions > 0 {
			row.AvgAttemptsPerPromotion = round2(float64(row.Attempts) / float64(row.Promotions))
		}
		out = append(out, *row)
	}
	sort.Slice(out, func(i, j int) bool {
		if out[i].Failures != out[j].Failures {
			return out[i].Failures > out[j].Failures
		}
		return out[i].FunctionTag < out[j].FunctionTag
	})

	resp := StuckMapResponse{
		WindowDays:  days,
		Rows:        out,
		GeneratedAt: now.Format(time.RFC3339),
		Note:        "内部运营视图：哪些意图类型反复卡壳，用于内容与难度迭代（86_ M4）。不外发。",
	}
	if s.rescues != nil {
		if rescues, err := s.rescues.CountRescuesByPath(ctx, userID, since); err == nil {
			resp.Rescues = rescues
		} else {
			s.logger.Warn("rescue counts unavailable for stuck map", "user_id", userID, "err", err)
		}
	}
	return resp, nil
}

// functionTags maps block id → function tag for the learner's live blocks.
func (s *Service) functionTags(ctx context.Context, userID string) map[string]string {
	blocks, err := s.blocks.ListBlocks(ctx, corpus.ListFilter{UserID: userID, Limit: 500})
	if err != nil {
		return map[string]string{}
	}
	out := make(map[string]string, len(blocks))
	for _, block := range blocks {
		if block.DeletedAt != nil {
			continue
		}
		out[block.ID] = block.FunctionTag
	}
	return out
}

func round2(v float64) float64 {
	return float64(int(v*100+0.5)) / 100
}
