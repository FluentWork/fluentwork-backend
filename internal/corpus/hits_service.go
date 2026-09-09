package corpus

import (
	"context"
	"strings"

	"github.com/FluentWork/fluentwork-backend/internal/apierr"
)

const (
	// DefaultLookbackTurns is the recent-hits window when the client omits lookback_turns.
	DefaultLookbackTurns = 8
	// DefaultMinScore is accepted for 48_ §2.7 compatibility. Phrase blocks do
	// not persist a score column; recorded hits already passed B7 detection.
	DefaultMinScore = 0.7
	// RecentHitTTLMs is added to the newest hit's used_at_ms to produce ttl_at_ms.
	RecentHitTTLMs = int64(60_000)
)

// Hit is one B7 detection the voice-gateway reports for a turn.
type Hit struct {
	BlockID      string
	TurnID       string
	DetectedAtMs int64
}

// RecentHit is one ledger row joined to its phrase block for LLM prompt injection.
type RecentHit struct {
	BlockID  string `json:"block_id"`
	TurnID   string `json:"turn_id"`
	UsedAtMs int64  `json:"used_at_ms"`
	IntentZH string `json:"intent_zh"`
	ChunkEN  string `json:"chunk_en"`
}

// RecentHitsResult is the GET recent-hits payload.
type RecentHitsResult struct {
	Hits    []RecentHit `json:"hits"`
	TTLAtMs int64       `json:"ttl_at_ms"`
}

// HitsService records and queries the B7 hit ledger.
type HitsService struct {
	store Store
}

// NewHitsService constructs a HitsService over store.
func NewHitsService(store Store) *HitsService {
	return &HitsService{store: store}
}

// RecordHits UPSERTs ledger rows for one turn and increments phrase_blocks.total_uses
// only on first insert of (session_id, turn_id, block_id).
func (s *HitsService) RecordHits(ctx context.Context, userID, sessionID, turnID string, hits []Hit) (int, error) {
	userID = strings.TrimSpace(userID)
	sessionID = strings.TrimSpace(sessionID)
	turnID = strings.TrimSpace(turnID)
	if userID == "" {
		return 0, apierr.InvalidArgument("user_id is required")
	}
	if sessionID == "" {
		return 0, apierr.InvalidArgument("session_id is required")
	}
	if turnID == "" {
		return 0, apierr.InvalidArgument("turn_id is required")
	}
	if s == nil || s.store == nil {
		return 0, apierr.Internal("hits store is not configured")
	}
	deduped := dedupeHits(turnID, hits)
	if len(deduped) == 0 {
		return 0, nil
	}
	return s.store.RecordHits(ctx, userID, sessionID, turnID, deduped)
}

// RecentHits returns hits from the most recent lookbackTurns in the session.
// minScore is accepted for the frozen contract and currently unused: the ledger
// has no score column, and voice-gateway only reports hits that already passed
// the detector threshold.
func (s *HitsService) RecentHits(ctx context.Context, sessionID string, lookbackTurns int, minScore float64) (RecentHitsResult, error) {
	sessionID = strings.TrimSpace(sessionID)
	if sessionID == "" {
		return RecentHitsResult{}, apierr.InvalidArgument("session_id is required")
	}
	if s == nil || s.store == nil {
		return RecentHitsResult{}, apierr.Internal("hits store is not configured")
	}
	_ = minScore
	if lookbackTurns < 0 {
		lookbackTurns = 0
	}
	hits, err := s.store.ListSessionHits(ctx, sessionID)
	if err != nil {
		return RecentHitsResult{}, err
	}
	filtered := applyLookback(hits, lookbackTurns)
	ttl := int64(0)
	if len(filtered) > 0 {
		ttl = filtered[0].UsedAtMs + RecentHitTTLMs
	}
	if filtered == nil {
		filtered = []RecentHit{}
	}
	return RecentHitsResult{Hits: filtered, TTLAtMs: ttl}, nil
}

// RecordHits forwards B7 hit reporting onto HitsService.
func (s *Service) RecordHits(ctx context.Context, userID, sessionID, turnID string, hits []Hit) (int, error) {
	if s == nil {
		return 0, apierr.Internal("corpus service is not configured")
	}
	return NewHitsService(s.store).RecordHits(ctx, userID, sessionID, turnID, hits)
}

// RecentHits forwards recent-hit lookup onto HitsService.
func (s *Service) RecentHits(ctx context.Context, sessionID string, lookbackTurns int, minScore float64) (RecentHitsResult, error) {
	if s == nil {
		return RecentHitsResult{}, apierr.Internal("corpus service is not configured")
	}
	return NewHitsService(s.store).RecentHits(ctx, sessionID, lookbackTurns, minScore)
}

func dedupeHits(turnID string, hits []Hit) []Hit {
	type seenHit struct {
		hit   Hit
		order int
	}
	seen := make(map[string]seenHit, len(hits))
	order := 0
	for _, hit := range hits {
		hit.BlockID = strings.TrimSpace(hit.BlockID)
		if hit.BlockID == "" {
			continue
		}
		if strings.TrimSpace(hit.TurnID) == "" {
			hit.TurnID = turnID
		}
		if existing, ok := seen[hit.BlockID]; ok {
			if hit.DetectedAtMs >= existing.hit.DetectedAtMs {
				seen[hit.BlockID] = seenHit{hit: hit, order: existing.order}
			}
			continue
		}
		seen[hit.BlockID] = seenHit{hit: hit, order: order}
		order++
	}
	out := make([]Hit, len(seen))
	for _, item := range seen {
		out[item.order] = item.hit
	}
	return out
}

func applyLookback(hits []RecentHit, lookbackTurns int) []RecentHit {
	if lookbackTurns <= 0 || len(hits) == 0 {
		return []RecentHit{}
	}
	allowed := make(map[string]struct{}, lookbackTurns)
	for _, hit := range hits {
		if _, ok := allowed[hit.TurnID]; ok {
			continue
		}
		if len(allowed) >= lookbackTurns {
			continue
		}
		allowed[hit.TurnID] = struct{}{}
	}
	out := make([]RecentHit, 0, len(hits))
	for _, hit := range hits {
		if _, ok := allowed[hit.TurnID]; ok {
			out = append(out, hit)
		}
	}
	return out
}
