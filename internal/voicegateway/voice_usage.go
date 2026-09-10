package voicegateway

// VoiceUsage is the audio a gateway session moved, per direction, in
// milliseconds.
//
// This is what the voice path can contribute to cost accounting today. The
// gateway sees exactly the bytes it forwarded and nothing else — the duplex
// protocol reports no token counts, so bytes are the only unit available here.
//
// A **measurement**, deliberately not a price. The vendor's billing unit is
// unverified: the published doc contradicts itself on the output-text rate by
// 2.67x (meta 77_ P2-2, "以账单为准"). A wrong number in a cost ledger reads as
// authoritative — it gets used for pricing and for circuit-breaking, and nobody
// re-derives it. Record the fact now; fill in money when the bill settles it.
// `aicost.Log.CostFen` stays 0 for these rows until then.
type VoiceUsage struct {
	UplinkMS   int64 `json:"uplink_ms"`
	DownlinkMS int64 `json:"downlink_ms"`
}

// Wire-format rates. Both directions are mono s16le, so bytes per millisecond
// is sampleRate × 2 ÷ 1000.
const (
	// uplinkSampleRate is what the client captures at: `LiveAudioEngine` builds
	// every frame at 16 kHz mono, and the gateway forwards those bytes as-is.
	uplinkSampleRate = 16000

	uplinkBytesPerMS   = int64(uplinkSampleRate) * 2 / 1000
	downlinkBytesPerMS = int64(duplexOutputRate) * 2 / 1000
)

// voiceUsage accumulates the bytes a session moved.
//
// Kept separate from the session struct so the arithmetic — which is the part
// that can be silently wrong — is testable without a socket, the same reason
// `AudioFrameDropPolicy` sits outside `AudioFrameDropGate`.
type voiceUsage struct {
	uplinkBytes   int64
	downlinkBytes int64
}

func (u *voiceUsage) addUplink(n int) {
	if n > 0 {
		u.uplinkBytes += int64(n)
	}
}

func (u *voiceUsage) addDownlink(n int) {
	if n > 0 {
		u.downlinkBytes += int64(n)
	}
}

// measure converts accumulated bytes to milliseconds. Integer division floors,
// which is the honest direction: a partial millisecond is not a millisecond of
// audio, and rounding up would inflate every session by up to one.
func (u voiceUsage) measure() VoiceUsage {
	return VoiceUsage{
		UplinkMS:   u.uplinkBytes / uplinkBytesPerMS,
		DownlinkMS: u.downlinkBytes / downlinkBytesPerMS,
	}
}

// VoiceUsageReporter is implemented by provider sessions that can say how much
// audio they moved.
//
// Optional, like SequencedVoiceProviderSession: Mock and DevEcho do not carry a
// real conversation, so there is nothing for them to report and no reason to
// make every provider answer.
type VoiceUsageReporter interface {
	VoiceProviderSession
	VoiceUsage() VoiceUsage
}
