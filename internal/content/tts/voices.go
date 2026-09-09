package tts

import "strings"

// D-2 voice catalog. VoiceID is the Volc speaker id passed as req_params.speaker.
// X-Api-Resource-Id stays the product SKU (seed-tts-2.0) from VolcStreamingProvider.
// Override a speaker with VOLC_VOICE_* env vars via LookupVoice aliases after C-2 lookup.
const (
	VoiceIDAIMaleTech        = "zh_male_tech_01"
	VoiceIDAIFemalePro       = "en_female_professional"
	VoiceIDDailyReadNarrator = "en_male_narrator"
	VoiceIDDrillCountdown    = "en_female_clear"
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
)

// Catalog returns the four frozen D-2 voices.
func Catalog() []VoiceConfig {
	return []VoiceConfig{
		VoiceAIMaleTech,
		VoiceAIFemalePro,
		VoiceDailyReadNarrator,
		VoiceDrillCountdown,
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
	default:
		return VoiceConfig{VoiceID: strings.TrimSpace(id)}.WithDefaults()
	}
}
