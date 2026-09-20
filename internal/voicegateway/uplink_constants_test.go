package voicegateway

import "testing"

// TestUplinkChunkBytesDerivedFromClientFormat ensures UplinkChunkBytes matches
// the frame size derived from ClientAudioFormat for 20ms chunks.
//
// This test locks the relationship: when ClientAudioFormat changes (sample rate,
// channels, bits per sample), UplinkChunkBytes must be updated to match.
func TestUplinkChunkBytesDerivedFromClientFormat(t *testing.T) {
	expected := ClientAudioFormat.FrameBytes(20)
	if UplinkChunkBytes != expected {
		t.Errorf("UplinkChunkBytes = %d, want %d (ClientAudioFormat.FrameBytes(20))",
			UplinkChunkBytes, expected)
	}
}
