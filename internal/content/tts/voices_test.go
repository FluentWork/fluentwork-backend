package tts

import "testing"

func TestLookupVoice_CatalogAndPassthrough(t *testing.T) {
	if got := LookupVoice(""); got != VoiceAIFemalePro {
		t.Fatalf("empty = %+v, want default female pro", got)
	}
	if got := LookupVoice(VoiceIDAIMaleTech); got.VoiceID != VoiceIDAIMaleTech || got.Speed != 0.9 {
		t.Fatalf("male tech = %+v", got)
	}
	if got := LookupVoice("daily_read_narrator"); got != VoiceDailyReadNarrator {
		t.Fatalf("alias = %+v", got)
	}
	if got := LookupVoice("zh_female_vv_jupiter_bigtts"); got.VoiceID != "zh_female_vv_jupiter_bigtts" || got.Speed != DefaultSpeed {
		t.Fatalf("passthrough = %+v", got)
	}
	if len(Catalog()) != 4 {
		t.Fatalf("catalog len = %d, want 4", len(Catalog()))
	}
}
