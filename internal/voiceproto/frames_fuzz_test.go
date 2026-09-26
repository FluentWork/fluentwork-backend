package voiceproto_test

import (
	"bytes"
	"encoding/binary"
	"testing"

	"github.com/FluentWork/fluentwork-backend/internal/voiceproto"
)

func FuzzDecodeAITTSAudio(f *testing.F) {
	seeds := []struct {
		raw []byte
		h8  bool
	}{
		{raw: []byte{0x00, 0x00, 0x00, 0x01, 0xAA}},
		{raw: []byte{0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x01, 0xAA}, h8: true},
		{raw: []byte{0x00, 0x00, 0x00, 0x01, 0x00, 0x00, 0x00, 0x00, 0xAA}, h8: true},
		{raw: []byte{0xFF, 0xFF, 0xFF, 0xFF, 0x00, 0x00, 0x00, 0x00, 0x01}, h8: true},
		{raw: []byte{}},
		{raw: []byte{}, h8: true},
		{raw: []byte{0x01, 0x02, 0x03, 0x04}},
		{raw: []byte{0x01, 0x02, 0x03, 0x04}, h8: true},
		{raw: []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}},
		{raw: []byte{0x01, 0x02, 0x03, 0x04, 0x05, 0x06, 0x07, 0x08}, h8: true},
	}
	for _, seed := range seeds {
		f.Add(seed.raw, seed.h8)
	}

	f.Fuzz(func(t *testing.T, raw []byte, h8 bool) {
		layout := voiceproto.AudioFrameLayoutH4
		if h8 {
			layout = voiceproto.AudioFrameLayoutH8
		}
		header := layout.HeaderBytes()

		frame, err := voiceproto.DecodeAITTSAudio(raw, layout)
		if err != nil {
			if len(raw) >= header+1 {
				t.Fatalf("rejected a frame that carries a payload: layout=%v len=%d err=%v", layout, len(raw), err)
			}
			return
		}

		if len(raw) < header+1 {
			t.Fatalf("accepted a frame with no room for a payload: layout=%v len=%d", layout, len(raw))
		}
		if len(frame.Payload) != len(raw)-header {
			t.Fatalf("payload is %d bytes, want %d", len(frame.Payload), len(raw)-header)
		}
		if len(frame.Payload) == 0 {
			t.Fatal("accepted a frame with an empty payload")
		}
		if want := binary.BigEndian.Uint32(raw[:4]); frame.Seq != want {
			t.Fatalf("seq = %d, want %d", frame.Seq, want)
		}
		if h8 {
			if frame.TurnRef == nil {
				t.Fatal("h8 frame decoded without a turn_ref")
			}
			if want := binary.BigEndian.Uint32(raw[4:8]); *frame.TurnRef != want {
				t.Fatalf("turn_ref = %d, want %d", *frame.TurnRef, want)
			}
		} else if frame.TurnRef != nil {
			t.Fatalf("h4 frame decoded with a turn_ref (%d) it cannot carry", *frame.TurnRef)
		}

		encoded, err := frame.Encode()
		if err != nil {
			t.Fatalf("re-encode of a decoded frame failed: %v", err)
		}
		if !bytes.Equal(encoded, raw) {
			t.Fatalf("round trip changed the bytes: in=%x out=%x", raw, encoded)
		}
	})
}
