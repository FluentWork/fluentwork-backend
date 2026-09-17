package tts

import "strings"

// D-2 voice catalog. VoiceID is the Volc speaker id passed as req_params.speaker.
// X-Api-Resource-Id stays the product SKU (seed-tts-2.0) from VolcStreamingProvider.
//
// Every id here must be a speaker the account's resource actually serves. All of
// them are 2.0 speakers (`*_uranus_bigtts`) because seed-tts-2.0 serves only the
// 2.0 lineage; a 1.0 name like `en_female_professional` synthesizes nothing and
// fails as code 55000000, which reads like a missing entitlement rather than a
// wrong name (docs/92 §2, ErrSpeakerResourceMismatch). The four placeholders
// that used to be here were never real speakers.
const (
	VoiceIDAIMaleTech        = "zh_male_m191_uranus_bigtts"
	VoiceIDAIFemalePro       = "zh_female_vv_uranus_bigtts"
	VoiceIDDailyReadNarrator = "zh_male_shenyeboke_uranus_bigtts"
	VoiceIDDrillCountdown    = "zh_female_meilinvyou_uranus_bigtts"
)

var (
	// VoiceAIMaleTech is the male technical coach voice (D-2).
	VoiceAIMaleTech = VoiceConfig{VoiceID: VoiceIDAIMaleTech, Speed: 0.9}
	// VoiceAIFemalePro is the default female professional voice (D-2).
	VoiceAIFemalePro = VoiceConfig{VoiceID: VoiceIDAIFemalePro, Speed: 1.0}
	// VoiceDailyReadNarrator is the daily-read narrator voice (D-2).
	VoiceDailyReadNarrator = VoiceConfig{VoiceID: VoiceIDDailyReadNarrator, Speed: 0.85}
	// VoiceDrillCountdown is the drill countdown voice (D-2).
	VoiceDrillCountdown = VoiceConfig{VoiceID: VoiceIDDrillCountdown, Speed: 1.0}
	// VoiceRescueLadder is the stuck-rescue voice: the same speaker as the
	// default, deliberately. The ladder is the coach leaning in, not a different
	// person, so the voice stays and only the pace changes (PRD §5.4.3 约束 2).
	// Speed is a synthesis parameter here because the client cannot slow down PCM
	// it is already streaming.
	VoiceRescueLadder = VoiceConfig{VoiceID: VoiceIDAIFemalePro, Speed: 0.9}
)

// Catalog returns the frozen D-2 voices.
func Catalog() []VoiceConfig {
	return []VoiceConfig{
		VoiceAIMaleTech,
		VoiceAIFemalePro,
		VoiceDailyReadNarrator,
		VoiceDrillCountdown,
		VoiceRescueLadder,
	}
}

// LookupVoice resolves a client voice_id to a VoiceConfig.
// Empty id uses VoiceAIFemalePro. Unknown ids are passed through so callers can
// send a raw Volc speaker after C-2 lookup without a code change.
func LookupVoice(id string) VoiceConfig {
	switch strings.TrimSpace(id) {
	case "", "ai_female_pro":
		return VoiceAIFemalePro
	case VoiceIDAIMaleTech, "ai_male_tech":
		return VoiceAIMaleTech
	case VoiceIDAIFemalePro:
		return VoiceAIFemalePro
	case VoiceIDDailyReadNarrator, "daily_read_narrator":
		return VoiceDailyReadNarrator
	case VoiceIDDrillCountdown, "drill_countdown":
		return VoiceDrillCountdown
	case "rescue_ladder":
		return VoiceRescueLadder
	default:
		return VoiceConfig{VoiceID: strings.TrimSpace(id)}.WithDefaults()
	}
}
