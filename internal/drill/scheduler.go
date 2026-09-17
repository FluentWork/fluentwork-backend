package drill

import (
	"context"
	"sort"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/corpus"
)

// RoundOptions tunes one selection pass (PRD §5.3.2's 每日新块释放上限).
type RoundOptions struct {
	// IncludeNew lets 灰 blocks into the round. False once the day's new-block
	// budget is spent: training and automated material still comes, so the
	// limit defers new blocks instead of emptying the drill.
	IncludeNew bool
}

// DefaultRoundOptions is the uncapped behaviour: every due block may appear.
func DefaultRoundOptions() RoundOptions {
	return RoundOptions{IncludeNew: true}
}

// SelectBlocksForRound returns up to size due cards: new/training first, then automated.
func SelectBlocksForRound(ctx context.Context, store corpus.Store, userID string, now time.Time, size int, opts RoundOptions) ([]corpus.PhraseBlock, error) {
	if size <= 0 {
		return []corpus.PhraseBlock{}, nil
	}
	primaryStates := []string{corpus.StateNew, corpus.StateTraining}
	if !opts.IncludeNew {
		primaryStates = []string{corpus.StateTraining}
	}
	primary, err := store.ListDueBlocks(ctx, userID, now, primaryStates, size)
	if err != nil {
		return nil, err
	}
	if len(primary) >= size {
		return primary[:size], nil
	}
	need := size - len(primary)
	extra, err := store.ListDueBlocks(ctx, userID, now, []string{corpus.StateAutomated}, need)
	if err != nil {
		return nil, err
	}
	out := append(primary, extra...)
	sort.SliceStable(out, func(i, j int) bool {
		if out[i].NextDueAt.Equal(out[j].NextDueAt) {
			return out[i].ID < out[j].ID
		}
		return out[i].NextDueAt.Before(out[j].NextDueAt)
	})
	if len(out) > size {
		out = out[:size]
	}
	return out, nil
}
