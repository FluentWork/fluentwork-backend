package voicegateway_test

import (
	"bytes"
	"encoding/binary"
	"os"
	"path/filepath"
	"testing"

	"github.com/FluentWork/fluentwork-backend/internal/voicegateway"
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
