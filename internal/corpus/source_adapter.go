package corpus

import (
	"context"
	"fmt"

	"github.com/FluentWork/fluentwork-backend/internal/session"
)

// BlockSourceAdapter exposes the corpus service as a session.BlockSource so
// the voice gateway hit detector can score a user's spoken text against the
// user's stored phrase blocks without depending on this package directly.
//
// The adapter enforces the same cap (50) that session.HitDetector expects
// internally; if more candidates are ever needed the cap should be lifted in
// both places together.
type BlockSourceAdapter struct {
	Service *Service
}

// NewBlockSourceAdapter constructs an adapter over the corpus service.
func NewBlockSourceAdapter(service *Service) *BlockSourceAdapter {
	return &BlockSourceAdapter{Service: service}
}

// CandidatesForUser returns the non-deleted phrase blocks owned by userID,
// scoped to the most-recently-created window that the detector is willing
// to consider (50 rows). The service's keyword filter is intentionally left
// empty — the detector does its own token scoring and would otherwise be
// crippled by a one-token keyword prefetch.
//
// compile-time assertion that adapter satisfies session.BlockSource.
var _ session.BlockSource = (*BlockSourceAdapter)(nil)

// CandidatesForUser implements session.BlockSource.
func (a *BlockSourceAdapter) CandidatesForUser(ctx context.Context, userID string) ([]session.BlockCandidate, error) {
	if a == nil || a.Service == nil {
		return nil, fmt.Errorf("corpus source adapter: service is nil")
	}
	resp, err := a.Service.ListBlocks(ctx, ListBlocksRequest{
		UserID: userID,
		Limit:  SourceCandidateLimit,
	})
	if err != nil {
		return nil, err
	}
	out := make([]session.BlockCandidate, 0, len(resp.Items))
	for _, item := range resp.Items {
		out = append(out, session.BlockCandidate{
			ID:             item.ID,
			ExpressionEN:   item.ExpressionEN,
			IntentZH:       item.IntentZH,
			AnchorUserSaid: item.AnchorUserSaid,
			SceneTag:       item.SceneTag,
			FunctionTag:    item.FunctionTag,
		})
	}
	return out, nil
}

// SourceCandidateLimit is the per-user fetch cap surfaced from the corpus to
// the voice hit detector. Mirrors session.hitCandidateCap so the two stay in
// lockstep.
const SourceCandidateLimit = 50
