package content

import (
	"context"

	"github.com/FluentWork/fluentwork-backend/internal/corpus"
)

// CorpusBlockSource adapts corpus.Store for daily read generation.
type CorpusBlockSource struct {
	Store corpus.Store
}

// ListRecentBlocks implements BlockSource.
func (s CorpusBlockSource) ListRecentBlocks(ctx context.Context, userID string, limit int) ([]SourceBlock, error) {
	if s.Store == nil {
		return nil, nil
	}
	if limit <= 0 {
		limit = maxBlocksInDailyRead
	}
	blocks, err := s.Store.ListBlocks(ctx, corpus.ListFilter{
		UserID: userID,
		Limit:  limit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]SourceBlock, 0, len(blocks))
	for _, block := range blocks {
		out = append(out, SourceBlock{
			ID:             block.ID,
			IntentZH:       block.IntentZH,
			ExpressionEN:   block.ExpressionEN,
			AnchorUserSaid: block.AnchorUserSaid,
			SceneTag:       block.SceneTag,
			FunctionTag:    block.FunctionTag,
		})
	}
	return out, nil
}
