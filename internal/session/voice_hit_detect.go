package session

import (
	"context"
	"errors"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/voiceproto"
)

// HitDetectRequest is the input for one B7 hit-detection call (B12).
//
// Text is the user's just-spoken transcription (ASR result). SessionID and
// TurnID identify the conversation turn so the emitted badge frame can be
// deduped by the gateway.
type HitDetectRequest struct {
	UserID    string
	SessionID string
	TurnID    string
	Text      string
}

// BlockCandidate is the minimum corpus row shape the hit detector needs to
// score a hit. It is intentionally decoupled from corpus.PhraseBlock so this
// package does not import internal/corpus (and so the detector stays
// trivially testable with hand-built fakes).
type BlockCandidate struct {
	ID             string
	ExpressionEN   string
	IntentZH       string
	AnchorUserSaid string
	SceneTag       string
	FunctionTag    string
}

// BlockSource returns the corpus candidates that should be considered for hit
// detection on a single user turn. Implementations are expected to scope by
// user (and ideally by what is realistically loadable inside the 800ms budget
// — the detector itself never reaches into storage).
type BlockSource interface {
	CandidatesForUser(ctx context.Context, userID string) ([]BlockCandidate, error)
}

// HitDecision is the outcome of Detect: either a Hit with the badge payload
// the gateway should emit, or Miss.
type HitDecision struct {
	Kind     HitDecisionKind
	Hit      Hit
	Duration time.Duration
}

// HitDecisionKind enumerates Detect outcomes.
type HitDecisionKind int

const (
	// HitDecisionMiss means no candidate scored above the threshold.
	HitDecisionMiss HitDecisionKind = iota
	// HitDecisionHit means exactly one candidate crossed the threshold and
	// the gateway should emit feedback.badge for it.
	HitDecisionHit
)

// Hit is the materialized payload the gateway will turn into a
// voiceproto.FeedbackBadge frame.
type Hit struct {
	BlockCandidate
	Score      float64
	BadgeLabel string
	Tier       string
}

// ErrHitDetectInvalidRequest is returned when a request lacks the minimum
// fields needed to score (empty user / empty text). Callers treat this as
// "no hit" and log at debug.
var ErrHitDetectInvalidRequest = errors.New("voice hit-detect: invalid request")

// Default scoring thresholds and caps. PR2 / tuning tickets may lift these
// out into config when B14 POC tier结论 lands — they live as package vars so
// tests can override per-case.
var (
	// hitMinScore is the lower bound on the token-overlap score required to
	// treat a candidate as a hit. 0.65 chosen so single-token partial
	// overlaps (1/3 ≈ 0.33) never trigger; full single-token overlap (1.0)
	// always triggers.
	hitMinScore = 0.65
	// hitCandidateCap bounds how many candidates are considered per turn.
	// The detector picks the top-N by length-comparable ExpressionEN and
	// never paginates — the caller is responsible for pre-filtering.
	hitCandidateCap = 50
	// hitMaxTextLen guards against runaway ASR transcripts.
	hitMaxTextLen = 4 * 1024
)

// NewHitDetector constructs a pure-function detector. The BlockSource is the
// only side-effect surface and is invoked once per Detect call.
func NewHitDetector(source BlockSource) *HitDetector {
	return &HitDetector{source: source}
}

// HitDetector scores one turn's transcript against a BlockSource and decides
// whether to emit a feedback.badge. It owns no goroutines, no LRU, and no
// persistence — the caller (gateway handler) handles LRU dedupe and async
// dispatch.
type HitDetector struct {
	source BlockSource
}

// Detect is the single public entry point. It honors ctx (the gateway must
// wrap the call in an 800ms timeout) and is safe to call from many
// goroutines because BlockSource is the only shared resource.
func (d *HitDetector) Detect(ctx context.Context, req HitDetectRequest) (HitDecision, error) {
	start := time.Now()
	if err := validateHitRequest(req); err != nil {
		return HitDecision{Kind: HitDecisionMiss, Duration: time.Since(start)}, err
	}

	rawCandidates, err := d.source.CandidatesForUser(ctx, req.UserID)
	if err != nil {
		return HitDecision{Kind: HitDecisionMiss, Duration: time.Since(start)}, fmt.Errorf("load candidates: %w", err)
	}
	if len(rawCandidates) == 0 {
		return HitDecision{Kind: HitDecisionMiss, Duration: time.Since(start)}, nil
	}

	candidates := trimCandidates(rawCandidates, hitCandidateCap)
	scored := scoreAll(req.Text, candidates)
	best := pickBest(scored, hitMinScore)
	if best == nil {
		return HitDecision{Kind: HitDecisionMiss, Duration: time.Since(start)}, nil
	}
	best.BadgeLabel = buildBadgeLabel(best.ExpressionEN, best.IntentZH)
	best.Tier = voiceproto.BadgeTierSoft
	return HitDecision{
		Kind:     HitDecisionHit,
		Hit:      *best,
		Duration: time.Since(start),
	}, nil
}

func validateHitRequest(req HitDetectRequest) error {
	if strings.TrimSpace(req.UserID) == "" {
		return fmt.Errorf("%w: user_id is required", ErrHitDetectInvalidRequest)
	}
	if strings.TrimSpace(req.Text) == "" {
		return fmt.Errorf("%w: text is required", ErrHitDetectInvalidRequest)
	}
	if len(req.Text) > hitMaxTextLen {
		return fmt.Errorf("%w: text exceeds %d bytes", ErrHitDetectInvalidRequest, hitMaxTextLen)
	}
	return nil
}

// trimCandidates caps the candidate set and prefers shorter ExpressionEN
// strings (cheap heuristic: short phrases are more likely to match a spoken
// utterance than long ones).
func trimCandidates(in []BlockCandidate, limit int) []BlockCandidate {
	if len(in) <= limit {
		return in
	}
	sorted := make([]BlockCandidate, len(in))
	copy(sorted, in)
	sort.SliceStable(sorted, func(i, j int) bool {
		return len(sorted[i].ExpressionEN) < len(sorted[j].ExpressionEN)
	})
	return sorted[:limit]
}

// tokenize lower-cases and strips non-alphanumeric noise so "I'll", "I'll"
// and "ill" all produce the same token set.
func tokenize(s string) []string {
	s = strings.ToLower(s)
	out := make([]string, 0, 8)
	var b strings.Builder
	for _, r := range s {
		switch {
		case r >= 'a' && r <= 'z', r >= '0' && r <= '9':
			b.WriteRune(r)
		default:
			if b.Len() > 0 {
				out = append(out, b.String())
				b.Reset()
			}
		}
	}
	if b.Len() > 0 {
		out = append(out, b.String())
	}
	return out
}

// scoreCandidate returns a similarity score in [0, 1] based on token overlap
// between the spoken text and the candidate's ExpressionEN. AnchorUserSaid
// is also considered (catches the "user previously said X" recall case from
// refine cards).
func scoreCandidate(text string, c BlockCandidate) float64 {
	textTokens := tokenize(text)
	if len(textTokens) == 0 {
		return 0
	}
	textSet := make(map[string]struct{}, len(textTokens))
	for _, t := range textTokens {
		textSet[t] = struct{}{}
	}
	expressionTokens := tokenize(c.ExpressionEN)
	anchorTokens := tokenize(c.AnchorUserSaid)
	combined := make([]string, 0, len(expressionTokens)+len(anchorTokens))
	combined = append(combined, expressionTokens...)
	combined = append(combined, anchorTokens...)
	if len(combined) == 0 {
		return 0
	}
	matched := 0
	seen := make(map[string]struct{}, len(combined))
	for _, t := range combined {
		if _, dup := seen[t]; dup {
			continue
		}
		seen[t] = struct{}{}
		if _, ok := textSet[t]; ok {
			matched++
		}
	}
	if matched == 0 {
		return 0
	}
	// Denominator is the smaller side so a short spoken hit on a long stored
	// expression still scores high (the user said exactly the key token).
	denom := len(combined)
	if len(textTokens) < denom {
		denom = len(textTokens)
	}
	return float64(matched) / float64(denom)
}

func scoreAll(text string, candidates []BlockCandidate) []Hit {
	out := make([]Hit, 0, len(candidates))
	for _, c := range candidates {
		score := scoreCandidate(text, c)
		if score <= 0 {
			continue
		}
		out = append(out, Hit{BlockCandidate: c, Score: score})
	}
	return out
}

func pickBest(scored []Hit, minScore float64) *Hit {
	if len(scored) == 0 {
		return nil
	}
	sort.SliceStable(scored, func(i, j int) bool {
		if scored[i].Score != scored[j].Score {
			return scored[i].Score > scored[j].Score
		}
		// Tie-break: prefer the block with fewer tokens (tighter match).
		return len(scored[i].ExpressionEN) < len(scored[j].ExpressionEN)
	})
	if scored[0].Score < minScore {
		return nil
	}
	return &scored[0]
}

func buildBadgeLabel(expressionEN, intentZH string) string {
	expressionEN = strings.TrimSpace(expressionEN)
	if expressionEN == "" {
		if intentZH == "" {
			return "Nice!"
		}
		return intentZH
	}
	return expressionEN
}
