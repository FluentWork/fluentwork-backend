package corpus

import (
	"context"
	"sort"
	"strings"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/apierr"
)

// Recommendation reasons. Each suggested block says why it is being suggested —
// a list the client cannot explain is a list the user learns to ignore.
const (
	// ReasonSceneMatch marks a block that belongs to the session's scene.
	ReasonSceneMatch = "scene_match"
	// ReasonMostUsed marks a block carried over from another scene because it is
	// the one this learner actually uses.
	ReasonMostUsed = "most_used"
	// ReasonStaleGreen marks a 绿 block that has not been revisited in a while.
	// PRD §5.3.1: 已自动化是低接触，不是免复习.
	ReasonStaleGreen = "stale_green"
)

const (
	// DefaultRecommendLimit is the 3 the PRD asks for when a session opens.
	DefaultRecommendLimit = 3
	// MaxRecommendLimit caps the query parameter.
	MaxRecommendLimit = 10
	// StaleAfter is how long a green block may go untouched before the corpus
	// suggests revisiting it (83_ §2.2 的遗忘风险).
	StaleAfter = 30 * 24 * time.Hour
	// recommendScanLimit bounds how much of the corpus one call reads. Ranking
	// happens in Go so both stores rank identically; the read stays bounded
	// because this runs when a session opens.
	recommendScanLimit = 200
)

// RecommendRequest is one corpus suggestion query.
type RecommendRequest struct {
	UserID      string
	SceneTag    string
	FunctionTag string
	Limit       int
}

// Recommendation is one suggested block plus the reason it is here.
type Recommendation struct {
	PhraseBlockView
	Reason string `json:"reason"`
	// Stale marks a green block past its review window.
	Stale bool `json:"stale"`
}

// RecommendationResponse is GET /corpus/recommendations.
type RecommendationResponse struct {
	SceneTag string           `json:"scene_tag,omitempty"`
	Items    []Recommendation `json:"items"`
}

// Recommend returns the blocks worth putting to work in this session.
//
// Ordering is product logic, not storage logic: scene match first, then how
// often the learner has actually used the block, then green over training, with
// id as the final tie-break so the answer is stable. It lives here rather than
// in each store because two implementations would be two rules.
func (s *Service) Recommend(ctx context.Context, req RecommendRequest) (RecommendationResponse, error) {
	userID := strings.TrimSpace(req.UserID)
	if userID == "" {
		return RecommendationResponse{}, apierr.Unauthenticated("missing authenticated user")
	}
	limit := req.Limit
	if limit <= 0 {
		limit = DefaultRecommendLimit
	}
	if limit > MaxRecommendLimit {
		limit = MaxRecommendLimit
	}
	if s == nil || s.store == nil {
		return RecommendationResponse{}, apierr.Internal("corpus store is not configured")
	}
	scene := strings.TrimSpace(req.SceneTag)
	function := strings.TrimSpace(req.FunctionTag)

	blocks, err := s.store.ListBlocks(ctx, ListFilter{UserID: userID, Limit: recommendScanLimit})
	if err != nil {
		return RecommendationResponse{}, err
	}
	now := s.now().UTC()
	inScene := make([]PhraseBlock, 0, len(blocks))
	rest := make([]PhraseBlock, 0, len(blocks))
	for _, block := range blocks {
		if block.DeletedAt != nil {
			continue
		}
		if function != "" && !strings.EqualFold(block.FunctionTag, function) {
			continue
		}
		if scene != "" && strings.EqualFold(block.SceneTag, scene) {
			inScene = append(inScene, block)
			continue
		}
		rest = append(rest, block)
	}
	sortByUsefulness(inScene)
	sortByUsefulness(rest)

	items := make([]Recommendation, 0, limit)
	for _, block := range inScene {
		if len(items) == limit {
			break
		}
		items = append(items, recommendation(block, scene, now))
	}
	for _, block := range rest {
		if len(items) == limit {
			break
		}
		items = append(items, recommendation(block, scene, now))
	}
	return RecommendationResponse{SceneTag: scene, Items: items}, nil
}

// sortByUsefulness orders one group: what the learner actually uses first, then
// green blocks, then the most recently touched, with id breaking ties so two
// calls with the same corpus return the same list.
func sortByUsefulness(blocks []PhraseBlock) {
	sort.SliceStable(blocks, func(i, j int) bool {
		a, b := blocks[i], blocks[j]
		if a.RealUseCount != b.RealUseCount {
			return a.RealUseCount > b.RealUseCount
		}
		aGreen := a.State == StateAutomated
		bGreen := b.State == StateAutomated
		if aGreen != bGreen {
			return aGreen
		}
		if !a.UpdatedAt.Equal(b.UpdatedAt) {
			return a.UpdatedAt.After(b.UpdatedAt)
		}
		return a.ID < b.ID
	})
}

// recommendation labels one block. A green block past its window is reported as
// a review prompt first: that is the more useful thing to say about it, even
// when it also matches the scene.
func recommendation(block PhraseBlock, scene string, now time.Time) Recommendation {
	stale := block.State == StateAutomated && referenceTime(block).Before(now.Add(-StaleAfter))
	switch {
	case stale:
		return Recommendation{PhraseBlockView: toView(block), Reason: ReasonStaleGreen, Stale: true}
	case scene != "" && strings.EqualFold(block.SceneTag, scene):
		return Recommendation{PhraseBlockView: toView(block), Reason: ReasonSceneMatch}
	default:
		return Recommendation{PhraseBlockView: toView(block), Reason: ReasonMostUsed}
	}
}

// referenceTime is the last moment this block was known to be alive: a real hit
// if there was one, otherwise the last schedule change.
func referenceTime(block PhraseBlock) time.Time {
	if block.LastUsedAt != nil {
		return block.LastUsedAt.UTC()
	}
	return block.UpdatedAt.UTC()
}
