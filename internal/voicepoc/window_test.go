package voicepoc

import (
	"context"
	"testing"
	"time"
)

func TestSummarizeTrialsTierSameTurn(t *testing.T) {
	trials := []InjectTrial{
		{DelayMS: 200, AffectedSameTurn: true},
		{DelayMS: 400, AffectedSameTurn: true},
		{DelayMS: 600, AffectedSameTurn: true},
		{DelayMS: 800, AffectedSameTurn: true},
		{DelayMS: 1000, AffectedSameTurn: false},
	}
	report := SummarizeTrials("mock", trials)
	if report.Tier != TierSameTurnConfirm {
		t.Fatalf("tier = %v want %v (p90=%d)", report.Tier, TierSameTurnConfirm, report.WindowP90MS)
	}
	if report.EffectiveMaxMS != 800 {
		t.Fatalf("effective max = %d", report.EffectiveMaxMS)
	}
}

func TestSummarizeTrialsTierNextTurn(t *testing.T) {
	trials := []InjectTrial{
		{DelayMS: 200, AffectedSameTurn: true},
		{DelayMS: 400, AffectedSameTurn: true},
		{DelayMS: 600, AffectedSameTurn: false},
		{DelayMS: 800, AffectedSameTurn: false},
	}
	report := SummarizeTrials("mock", trials)
	if report.Tier != TierNextTurnConfirm {
		t.Fatalf("tier = %v want %v (p90=%d)", report.Tier, TierNextTurnConfirm, report.WindowP90MS)
	}
}

func TestSummarizeTrialsTierBadgeOnly(t *testing.T) {
	trials := []InjectTrial{
		{DelayMS: 200, AffectedSameTurn: false},
		{DelayMS: 800, AffectedSameTurn: false},
	}
	report := SummarizeTrials("mock", trials)
	if report.Tier != TierBadgeOnly {
		t.Fatalf("tier = %v", report.Tier)
	}
}

func TestRunT9MockProvider(t *testing.T) {
	ctx := context.Background()
	report, err := RunT9(ctx, MockInjectionProvider{ModelStartAfter: 900 * time.Millisecond}, nil, 2)
	if err != nil {
		t.Fatal(err)
	}
	if len(report.Trials) != 10 {
		t.Fatalf("trials = %d", len(report.Trials))
	}
	if report.Tier != TierSameTurnConfirm {
		t.Fatalf("tier = %v p90=%d", report.Tier, report.WindowP90MS)
	}
	if report.CredentialMode != "mock" {
		t.Fatalf("credential mode = %q", report.CredentialMode)
	}
}

func TestSummarizeTrialsTierNextTurnFromNextTurnOnly(t *testing.T) {
	trials := []InjectTrial{
		{DelayMS: 200, AffectedSameTurn: false, AffectedNextTurn: true},
		{DelayMS: 800, AffectedSameTurn: false, AffectedNextTurn: true},
	}
	report := SummarizeTrials("volc-duplex", trials)
	if report.Tier != TierNextTurnConfirm {
		t.Fatalf("tier = %v want %v", report.Tier, TierNextTurnConfirm)
	}
	if report.CredentialMode != "live" {
		t.Fatalf("credential mode = %q", report.CredentialMode)
	}
	if report.NextTurnHitRate != 1 {
		t.Fatalf("next-turn hit rate = %v", report.NextTurnHitRate)
	}
}
