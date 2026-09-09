package topic

import (
	"context"
	"sort"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/corpus"
	"github.com/FluentWork/fluentwork-backend/internal/session"
)

// PracticeSignals builds generator input from corpus + recent sessions.
type PracticeSignals struct {
	Blocks   corpus.Store
	Sessions session.Store
}

// Snapshot implements SignalSource.
func (p PracticeSignals) Snapshot(ctx context.Context, userID string, now time.Time) (Signals, error) {
	out := Signals{
		SceneCounts:    map[string]int{},
		FunctionCounts: map[string]int{},
		Level:          "beginner",
		RecentTitles:   []string{},
		Empty:          true,
	}
	if p.Blocks != nil {
		blocks, err := p.Blocks.ListBlocks(ctx, corpus.ListFilter{UserID: userID, Limit: 200})
		if err != nil {
			return out, err
		}
		for _, b := range blocks {
			if b.SceneTag != "" {
				out.SceneCounts[b.SceneTag]++
			}
			if b.FunctionTag != "" {
				out.FunctionCounts[b.FunctionTag]++
			}
		}
		n := len(blocks)
		switch {
		case n >= 50:
			out.Level = "advanced"
		case n >= 10:
			out.Level = "intermediate"
		default:
			out.Level = "beginner"
		}
		if n > 0 {
			out.Empty = false
		}
	}
	if p.Sessions != nil {
		sessions, err := p.Sessions.ListSessions(ctx, userID, nil, "", 20)
		if err != nil {
			return out, err
		}
		cutoff := now.UTC().Add(-14 * 24 * time.Hour)
		seen := map[string]struct{}{}
		for _, sess := range sessions {
			if sess.CreatedAt.Before(cutoff) {
				continue
			}
			title := sess.SceneType
			if title == "" {
				continue
			}
			if _, ok := seen[title]; ok {
				continue
			}
			seen[title] = struct{}{}
			out.RecentTitles = append(out.RecentTitles, title)
			if len(out.RecentTitles) == 8 {
				break
			}
		}
		sort.Strings(out.RecentTitles)
	}
	return out, nil
}
