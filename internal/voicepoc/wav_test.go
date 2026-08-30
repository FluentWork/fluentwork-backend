package voicepoc

import (
	"testing"
)

func TestLoadWAVPCM16LE(t *testing.T) {
	pcm, rate, err := LoadWAVPCM16LE("testdata/cache_invalidation_16k.wav")
	if err != nil {
		t.Fatal(err)
	}
	if rate != 16000 {
		t.Fatalf("rate=%d", rate)
	}
	if len(pcm) < 16000 { // <1s would be suspicious for the fixture
		t.Fatalf("pcm too short: %d bytes", len(pcm))
	}
	if len(pcm)%2 != 0 {
		t.Fatalf("odd pcm length %d", len(pcm))
	}
}
