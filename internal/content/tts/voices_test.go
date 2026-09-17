package tts

import (
	"context"
	"errors"
	"testing"
)

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
	if got := LookupVoice("rescue_ladder"); got != VoiceRescueLadder {
		t.Fatalf("rescue alias = %+v", got)
	}
	if len(Catalog()) != 5 {
		t.Fatalf("catalog len = %d, want 5", len(Catalog()))
	}
}

// Every catalog speaker has to be one the account's resource actually serves, or
// the failure surfaces as a vendor code that reads like a missing entitlement
// (docs/92 §2). This pins the lineage half of that rule; the vendor half is what
// speakerMatchesResource checks at synthesis time.
func TestCatalog_SpeakersMatchTheConfiguredResource(t *testing.T) {
	resource := defaultVolcTTSResourceID
	for _, voice := range Catalog() {
		if !speakerMatchesResource(resource, voice.VoiceID) {
			t.Fatalf("catalog voice %q is not a speaker %s serves", voice.VoiceID, resource)
		}
	}
}

func TestSpeakerMatchesResource(t *testing.T) {
	cases := []struct {
		resource, speaker string
		want              bool
	}{
		{"seed-tts-2.0", "zh_female_vv_uranus_bigtts", true},
		{"seed-tts-2.0", "zh_female_vv_jupiter_bigtts", false},
		{"seed-tts-2.0", "en_female_professional", false},
		// An account on another SKU knows its own pairing; refusing on a guess
		// would replace the vendor's error with a wrong one of ours.
		{"some-other-sku", "en_female_professional", true},
		{"", "anything", true},
	}
	for _, tc := range cases {
		if got := speakerMatchesResource(tc.resource, tc.speaker); got != tc.want {
			t.Fatalf("speakerMatchesResource(%q, %q) = %v, want %v", tc.resource, tc.speaker, got, tc.want)
		}
	}
}

// The guard has to fire before the request goes out: a mismatch caught by the
// vendor costs a round trip and returns a code that sends you to the console.
func TestStream_RefusesSpeakerResourceMismatch(t *testing.T) {
	p := &VolcStreamingProvider{APIKey: "k", ResourceID: "seed-tts-2.0"}
	_, err := p.Stream(context.Background(), "hello", VoiceConfig{VoiceID: "en_female_professional"})
	if !errors.Is(err, ErrSpeakerResourceMismatch) {
		t.Fatalf("err = %v, want ErrSpeakerResourceMismatch", err)
	}
}
