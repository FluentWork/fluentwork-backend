package drill

import (
	"context"
	"sort"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/corpus"
)

// SelectBlocksForRound returns up to size due cards: new/training first, then automated.
func SelectBlocksForRound(ctx context.Context, store corpus.Store, userID string, now time.Time, size int) ([]corpus.PhraseBlock, error) {
	if size <= 0 {
		return []corpus.PhraseBlock{}, nil
	}
	primary, err := store.ListDueBlocks(ctx, userID, now, []string{corpus.StateNew, corpus.StateTraining}, size)
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

// ApplyJudge advances SM-2 state after one drill attempt (D-4 / T-B22-4).
//
// The ladder is implemented in corpus.ApplyJudge so that B7 hit writeback —
// which runs inside the corpus store transaction, where drill cannot be
// imported — shares the exact same rules. This delegate keeps drill's API.
func ApplyJudge(block corpus.PhraseBlock, pass bool, now time.Time) corpus.PhraseBlock {
	return corpus.ApplyJudge(block, pass, now)
}
