package session

import (
	"strings"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/aicost"
)

// voiceDuplexTaskType labels the ledger rows the voice path writes. Distinct
// from the review task type so the two can be summed separately — they are
// different vendor products with different rates.
const voiceDuplexTaskType = "voice.duplex"

// buildVoiceCostLog turns the gateway's measurement into a ledger row, or nil
// when there is nothing to record.
//
// **CostFen is deliberately 0.** The vendor's billing unit is unverified — its
// published doc contradicts itself on the output-text rate by 2.67x (meta 77_
// P2-2, "以账单为准"). A number computed from a guessed rate reads as
// authoritative in a ledger: it gets used for pricing and for circuit-breaking,
// and nobody re-derives it. Record the fact (audio seconds, model) now; fill in
// money when the bill settles it.
//
// Known limitation, recorded rather than hidden: `ai_cost_logs` has a single
// `audio_sec` column, so the uplink/downlink split the gateway sends is summed
// here and lost. That is fine while nothing is priced, and it is the thing to
// fix first if the two directions ever turn out to be priced differently — at
// which point this needs columns, not a different sum.
//
// Returns nil when usage is nil: a provider that cannot measure (mock,
// dev-echo) must leave **no row**, not a row of zeroes. A zero row would be
// indistinguishable from a real session that happened to move no audio, and the
// difference matters when the sum is used to decide whether anything ran.
func buildVoiceCostLog(session Session, usage *VoiceUsageItem, newID func() string, at time.Time) *aicost.Log {
	if usage == nil {
		return nil
	}
	// Negative inputs would silently reduce the total; the wire contract says
	// milliseconds, so clamp rather than trust.
	uplinkMS := max(usage.UplinkMS, 0)
	downlinkMS := max(usage.DownlinkMS, 0)

	return &aicost.Log{
		ID:       newID(),
		TaskType: voiceDuplexTaskType,
		Model:    strings.TrimSpace(usage.Model),
		// Integer division floors: a partial second is not a second of audio,
		// and rounding up would inflate every session.
		AudioSec:  int((uplinkMS + downlinkMS) / 1000),
		CostFen:   0,
		CreatedAt: at,
		UserID:    nullableUserID(session.UserID),
	}
}
