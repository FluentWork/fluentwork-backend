package topic

import (
	"context"
	"strings"

	"github.com/FluentWork/fluentwork-backend/internal/apierr"
	"github.com/FluentWork/fluentwork-backend/internal/corpus"
)

const (
	// DefaultStatsWindowDays is the 实战转化率 reporting window.
	DefaultStatsWindowDays = 30
	// MaxStatsWindowDays caps the query parameter.
	MaxStatsWindowDays = 180
	// statsBlockScanLimit bounds one stats read.
	statsBlockScanLimit = 500
)

// PracticeStats answers the question the moat rests on: 练到绿灯的表达，有多少
// 真的用出去了 (83_ §2.3 实战转化率).
//
// Two rates, two different questions:
//
//   - ConversionRate — of the expressions drilled to 绿, how many have been used
//     in a real conversation. This is the practice→reality half of the loop.
//   - CheckinRate — of the topics offered, how many were acted on. This is the
//     supply half: a topic nobody checks in on was not actionable.
//
// Both count *self-reported* reality plus in-app hits: a real meeting cannot be
// observed by the server (doc 84 §7.1). The numbers say so honestly rather than
// pretending to be telemetry.
type PracticeStats struct {
	WindowDays int `json:"window_days"`
	// Checkins is how many real conversations the learner reported.
	Checkins int `json:"checkins"`
	// CardsServed is how many topic cards were offered in the window.
	CardsServed int `json:"cards_served"`
	// BlocksTotal / BlocksUsed are the whole corpus.
	BlocksTotal int `json:"blocks_total"`
	BlocksUsed  int `json:"blocks_used"`
	// GreenBlocks / GreenUsed are the 绿 subset.
	GreenBlocks int `json:"green_blocks"`
	GreenUsed   int `json:"green_used"`
	// ConversionRate is GreenUsed/GreenBlocks, 0 when nothing is green yet.
	ConversionRate float64 `json:"conversion_rate"`
	// CheckinRate is Checkins/CardsServed, 0 when no cards were offered.
	CheckinRate float64 `json:"checkin_rate"`
	// RealUsesHit / RealUsesCheckin split the practice→reality evidence by how
	// it was observed (86_ M9): the first is what the server detected, the second
	// is what the learner reported. They are reported apart because a conversion
	// rate that mixes observation with self-report cannot say which loop works.
	RealUsesHit     int `json:"real_uses_hit"`
	RealUsesCheckin int `json:"real_uses_checkin"`
}

// PracticeStats builds the window summary for one learner.
func (s *Service) PracticeStats(ctx context.Context, userID string, days int) (PracticeStats, error) {
	userID = strings.TrimSpace(userID)
	if userID == "" {
		return PracticeStats{}, apierr.Unauthenticated("missing authenticated user")
	}
	if days <= 0 {
		days = DefaultStatsWindowDays
	}
	if days > MaxStatsWindowDays {
		days = MaxStatsWindowDays
	}
	now := s.now().UTC()
	since := now.AddDate(0, 0, -days)
	stats := PracticeStats{WindowDays: days}

	checkins, err := s.store.CountCheckinsSince(ctx, userID, since)
	if err != nil {
		return PracticeStats{}, err
	}
	stats.Checkins = checkins

	cards, err := s.store.CountCardsSince(ctx, userID, since)
	if err != nil {
		return PracticeStats{}, err
	}
	stats.CardsServed = cards

	// The corpus half is optional: without a block lookup the learner still gets
	// their checkin numbers rather than an error.
	if s.blocks != nil {
		blocks, err := s.blocks.ListBlocks(ctx, corpus.ListFilter{UserID: userID, Limit: statsBlockScanLimit})
		if err != nil {
			if s.logger != nil {
				s.logger.Warn("practice stats block lookup failed", "user_id", userID, "err", err)
			}
		} else {
			for _, block := range blocks {
				if block.DeletedAt != nil {
					continue
				}
				stats.BlocksTotal++
				if block.RealUseCount > 0 {
					stats.BlocksUsed++
				}
				if block.State == corpus.StateAutomated {
					stats.GreenBlocks++
					if block.RealUseCount > 0 {
						stats.GreenUsed++
					}
				}
			}
		}
	}
	if stats.BlocksTotal > 0 {
		stats.ConversionRate = rate(stats.GreenUsed, stats.GreenBlocks)
	}
	stats.CheckinRate = rate(stats.Checkins, stats.CardsServed)

	if s.realUses != nil {
		bySource, err := s.realUses.CountRealUsesBySource(ctx, userID, since)
		if err != nil {
			if s.logger != nil {
				s.logger.Warn("real use ledger unavailable", "user_id", userID, "err", err)
			}
		} else {
			stats.RealUsesHit = bySource["hit"]
			stats.RealUsesCheckin = bySource["checkin"]
		}
	}
	return stats, nil
}

// rate divides, and answers 0 for an empty denominator instead of NaN: a
// learner with nothing green yet has no conversion rate, not a broken number.
func rate(part, whole int) float64 {
	if whole <= 0 {
		return 0
	}
	return float64(part) / float64(whole)
}
