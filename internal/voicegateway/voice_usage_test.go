package voicegateway

import "testing"

// The arithmetic is the part that can be silently wrong. A factor-of-two error
// here under- or over-reports every session by 100%, and nothing downstream
// notices until a bill arrives and disagrees.
func TestVoiceUsageMeasuresEachDirectionAtItsOwnRate(t *testing.T) {
	t.Parallel()

	var usage voiceUsage
	usage.addUplink(32 * 1_000) // one second of 16 kHz mono s16le
	usage.addDownlink(48 * 500) // half a second of 24 kHz mono s16le

	got := usage.measure()
	if got.UplinkMS != 1_000 {
		t.Fatalf("uplink = %d ms, want 1000", got.UplinkMS)
	}
	if got.DownlinkMS != 500 {
		t.Fatalf("downlink = %d ms, want 500", got.DownlinkMS)
	}
}

// The two directions run at different rates (16 kHz up, 24 kHz down). Reusing
// one rate for both is the obvious mistake and it is invisible — the numbers
// stay plausible, they are just wrong by 1.5x in one direction.
func TestVoiceUsageDoesNotShareOneRateAcrossDirections(t *testing.T) {
	t.Parallel()

	var usage voiceUsage
	usage.addUplink(48_000)
	usage.addDownlink(48_000)

	got := usage.measure()
	if got.UplinkMS == got.DownlinkMS {
		t.Fatalf(
			"equal byte counts produced equal durations (%d ms each); the two directions are not the same rate",
			got.UplinkMS,
		)
	}
	if got.UplinkMS != 1_500 || got.DownlinkMS != 1_000 {
		t.Fatalf("got up=%d down=%d, want up=1500 down=1000", got.UplinkMS, got.DownlinkMS)
	}
}

// A frame that was dropped for being malformed or empty must not be billed as
// audio that reached the vendor.
func TestVoiceUsageIgnoresNonPositiveChunks(t *testing.T) {
	t.Parallel()

	var usage voiceUsage
	usage.addUplink(0)
	usage.addUplink(-1)
	usage.addDownlink(0)
	usage.addDownlink(-1)

	if got := usage.measure(); got.UplinkMS != 0 || got.DownlinkMS != 0 {
		t.Fatalf("non-positive chunks were counted: %+v", got)
	}
}

// Floor, not round. A partial millisecond is not a millisecond of audio, and
// rounding up inflates every session — the same direction of error the ledger
// must not have.
func TestVoiceUsageFloorsPartialMilliseconds(t *testing.T) {
	t.Parallel()

	var usage voiceUsage
	usage.addUplink(31) // one byte short of a millisecond at 32 B/ms

	if got := usage.measure().UplinkMS; got != 0 {
		t.Fatalf("partial millisecond reported as %d ms, want 0", got)
	}
}
