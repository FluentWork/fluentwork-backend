// Package voicepoc hosts Phase 0 B14 injection-window measurement helpers.
//
// Real Volcano sessions plug in behind InjectionProvider. The default mock
// provider lets the T9 delay-gradient harness run without vendor credentials
// and produces deterministic window statistics for pipeline verification.
package voicepoc

import (
	"context"
	"fmt"
	"sort"
	"time"
)

// Tier is the B7 fallback tier frozen by V8 / T9 (tech design §3.3).
type Tier int

const (
	// TierSameTurnConfirm: effective window P90 >= 800ms → confirm in the current turn.
	TierSameTurnConfirm Tier = 1
	// TierNextTurnConfirm: injection works but window is too short for same-turn → confirm next turn open.
	TierNextTurnConfirm Tier = 2
	// TierBadgeOnly: injection unavailable or never affects generation → badge only.
	TierBadgeOnly Tier = 3
)

func (t Tier) String() string {
	switch t {
	case TierSameTurnConfirm:
		return "① same-turn confirm"
	case TierNextTurnConfirm:
		return "② next-turn open confirm"
	case TierBadgeOnly:
		return "③ badge only"
	default:
		return fmt.Sprintf("unknown(%d)", int(t))
	}
}

// InjectTrial is one delayed-injection observation for T9.
type InjectTrial struct {
	DelayMS           int    `json:"delay_ms"`
	AffectedSameTurn  bool   `json:"affected_same_turn"`
	AffectedNextTurn  bool   `json:"affected_next_turn,omitempty"`
	ModelStartAfterMS int    `json:"model_start_after_ms"`
	SameTurnText      string `json:"same_turn_text,omitempty"`
	NextTurnText      string `json:"next_turn_text,omitempty"`
}

// WindowReport aggregates T9 trials into P50/P90 and a frozen B7 tier.
type WindowReport struct {
	Trials             []InjectTrial `json:"trials"`
	EffectiveMaxMS     int           `json:"effective_max_ms"`
	WindowP50MS        int           `json:"window_p50_ms"`
	WindowP90MS        int           `json:"window_p90_ms"`
	SameTurnHitRate    float64       `json:"same_turn_hit_rate"`
	NextTurnHitRate    float64       `json:"next_turn_hit_rate"`
	Tier               Tier          `json:"tier"`
	TierLabel          string        `json:"tier_label"`
	Provider           string        `json:"provider"`
	CredentialMode     string        `json:"credential_mode"`
	Notes              []string      `json:"notes,omitempty"`
}

// InjectionProvider abstracts the vendor session used by the POC harness.
type InjectionProvider interface {
	Name() string
	// RunDelayedInject performs one T9 trial: wait delay after VAD-stop, inject, observe same-turn effect.
	RunDelayedInject(ctx context.Context, delay time.Duration) (InjectTrial, error)
}

// RunT9 runs the delay gradient (≥6 trials per delay by default) and returns a WindowReport.
func RunT9(ctx context.Context, provider InjectionProvider, delays []time.Duration, trialsPerDelay int) (WindowReport, error) {
	if provider == nil {
		return WindowReport{}, fmt.Errorf("injection provider is required")
	}
	if trialsPerDelay <= 0 {
		trialsPerDelay = 6
	}
	if len(delays) == 0 {
		delays = []time.Duration{
			200 * time.Millisecond,
			400 * time.Millisecond,
			600 * time.Millisecond,
			800 * time.Millisecond,
			1000 * time.Millisecond,
		}
	}

	trials := make([]InjectTrial, 0, len(delays)*trialsPerDelay)
	for _, delay := range delays {
		for i := 0; i < trialsPerDelay; i++ {
			if err := ctx.Err(); err != nil {
				return WindowReport{}, err
			}
			trial, err := provider.RunDelayedInject(ctx, delay)
			if err != nil {
				return WindowReport{}, fmt.Errorf("delay=%s trial=%d: %w", delay, i+1, err)
			}
			trials = append(trials, trial)
		}
	}
	return SummarizeTrials(provider.Name(), trials), nil
}

// SummarizeTrials computes effective-window percentiles and maps them to a B7 tier.
func SummarizeTrials(provider string, trials []InjectTrial) WindowReport {
	effective := make([]int, 0, len(trials))
	maxEffective := 0
	sameHits := 0
	nextHits := 0
	for _, t := range trials {
		if t.AffectedSameTurn {
			sameHits++
			effective = append(effective, t.DelayMS)
			if t.DelayMS > maxEffective {
				maxEffective = t.DelayMS
			}
		}
		if t.AffectedNextTurn {
			nextHits++
		}
	}

	n := len(trials)
	report := WindowReport{
		Trials:         trials,
		EffectiveMaxMS: maxEffective,
		Provider:       provider,
		CredentialMode: "mock",
	}
	if n > 0 {
		report.SameTurnHitRate = float64(sameHits) / float64(n)
		report.NextTurnHitRate = float64(nextHits) / float64(n)
	}
	if provider != "" && provider != "mock" && provider != "mock-closed" {
		report.CredentialMode = "live"
	}

	if sameHits == 0 {
		if nextHits > 0 {
			report.Tier = TierNextTurnConfirm
			report.TierLabel = report.Tier.String()
			report.Notes = append(report.Notes,
				"no same-turn injection success; next-turn effect observed → tier ②")
			return report
		}
		report.Tier = TierBadgeOnly
		report.TierLabel = report.Tier.String()
		report.Notes = append(report.Notes, "no same-turn or next-turn injection success observed")
		return report
	}

	sort.Ints(effective)
	report.WindowP50MS = percentileNearestRank(effective, 50)
	report.WindowP90MS = percentileNearestRank(effective, 90)

	// Tech design §3.3 / doc 50: window >= 800ms → tier ①; otherwise tier ② when injection works at all.
	if report.WindowP90MS >= 800 {
		report.Tier = TierSameTurnConfirm
	} else {
		report.Tier = TierNextTurnConfirm
	}
	report.TierLabel = report.Tier.String()
	return report
}

func percentileNearestRank(sorted []int, p int) int {
	if len(sorted) == 0 {
		return 0
	}
	if p <= 0 {
		return sorted[0]
	}
	if p >= 100 {
		return sorted[len(sorted)-1]
	}
	rank := (p * len(sorted) + 99) / 100 // nearest-rank, 1-based
	if rank < 1 {
		rank = 1
	}
	if rank > len(sorted) {
		rank = len(sorted)
	}
	return sorted[rank-1]
}
