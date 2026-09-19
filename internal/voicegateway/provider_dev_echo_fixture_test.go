package voicegateway_test

import (
	"bytes"
	"context"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/FluentWork/fluentwork-backend/internal/voicegateway"
	"github.com/FluentWork/fluentwork-backend/internal/voiceproto"
)

func TestFixturePCMLoader_ReadsRawPCM(t *testing.T) {
	t.Parallel()

	pcm := voicegateway.DevEchoFixtureGenerator(20)
	path := filepath.Join(t.TempDir(), "sine.pcm")
	if err := os.WriteFile(path, pcm, 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := voicegateway.FixturePCMLoader(path)
	if err != nil {
		t.Fatalf("FixturePCMLoader: %v", err)
	}
	if !bytes.Equal(got, pcm) {
		t.Fatalf("raw PCM must be unchanged, got %d bytes want %d", len(got), len(pcm))
	}
}

func TestFixturePCMLoader_StripsWAVHeader(t *testing.T) {
	t.Parallel()

	pcm := voicegateway.DevEchoFixtureGenerator(20)
	path := filepath.Join(t.TempDir(), "sine.wav")
	if err := os.WriteFile(path, wrapPCM16WAV(pcm), 0o644); err != nil {
		t.Fatalf("write: %v", err)
	}

	got, err := voicegateway.FixturePCMLoader(path)
	if err != nil {
		t.Fatalf("FixturePCMLoader: %v", err)
	}
	if !bytes.Equal(got, pcm) {
		t.Fatalf("WAV payload mismatch, got %d bytes want %d", len(got), len(pcm))
	}
}

func TestFixturePCMLoader_MissingFile(t *testing.T) {
	t.Parallel()

	_, err := voicegateway.FixturePCMLoader(filepath.Join(t.TempDir(), "missing.pcm"))
	if err == nil {
		t.Fatal("expected error for missing file")
	}
}

// The fixture streams PCM to the client, and PCM is not a frame: the client
// reads a 4-byte big-endian sequence first (`WSAudioFrameCodec`). Sending the
// raw chunk made the first two samples the sequence number — random values, so
// a barge-in watermark recorded one of them and every later frame compared below
// it and was dropped as late. The audio path looked broken on the client while
// the client was reading the wire exactly as specified.
func TestFixtureAudioFramesCarryTheWireSequenceHeader(t *testing.T) {
	t.Parallel()

	provider := voicegateway.NewDevEchoVoiceProvider("", nil)
	provider.Fixture = voicegateway.DevEchoFixtureGenerator(40) // 2 × 20ms chunks

	sess, err := provider.Open(context.Background(), voicegateway.ConsumedTicket{SessionID: "s-fix"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close(context.Background()) })

	outbound, err := sess.HandleClientControl(
		context.Background(), voiceproto.TypeUserSpeechEnd, []byte(`{"turn_id":"t1"}`),
	)
	if err != nil {
		t.Fatalf("HandleClientControl: %v", err)
	}

	var seqs []uint32
	var payloads [][]byte
	for _, item := range outbound {
		if item.Binary == nil {
			continue
		}
		if len(item.Binary) <= 4 {
			t.Fatalf("frame is header-sized only: %d bytes", len(item.Binary))
		}
		seqs = append(seqs, binary.BigEndian.Uint32(item.Binary[:4]))
		payloads = append(payloads, item.Binary[4:])
	}
	if len(seqs) != 2 {
		t.Fatalf("got %d audio frames, want 2", len(seqs))
	}
	if seqs[0] != 0 || seqs[1] != 1 {
		t.Fatalf("sequences must be monotonic from 0, got %v", seqs)
	}
	for i, payload := range payloads {
		if len(payload)%2 != 0 {
			t.Fatalf("payload %d has an odd byte count: %d — not PCM16", i, len(payload))
		}
	}
}

func wrapPCM16WAV(pcm []byte) []byte {
	header := make([]byte, 44)
	copy(header[0:4], "RIFF")
	binary.LittleEndian.PutUint32(header[4:], uint32(36+len(pcm)))
	copy(header[8:12], "WAVE")
	copy(header[12:16], "fmt ")
	binary.LittleEndian.PutUint32(header[16:], 16)
	binary.LittleEndian.PutUint16(header[20:], 1)
	binary.LittleEndian.PutUint16(header[22:], 1)
	binary.LittleEndian.PutUint32(header[24:], 16000)
	binary.LittleEndian.PutUint32(header[28:], 32000)
	binary.LittleEndian.PutUint16(header[32:], 2)
	binary.LittleEndian.PutUint16(header[34:], 16)
	copy(header[36:40], "data")
	binary.LittleEndian.PutUint32(header[40:], uint32(len(pcm)))
	return append(header, pcm...)
}
