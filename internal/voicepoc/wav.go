package voicepoc

import (
	"encoding/binary"
	"fmt"
	"os"
)

// LoadWAVPCM16LE reads a mono PCM WAV and returns raw s16le samples (no header).
func LoadWAVPCM16LE(path string) (pcm []byte, sampleRate int, err error) {
	raw, err := os.ReadFile(path)
	if err != nil {
		return nil, 0, err
	}
	if len(raw) < 44 || string(raw[0:4]) != "RIFF" || string(raw[8:12]) != "WAVE" {
		return nil, 0, fmt.Errorf("not a WAVE file: %s", path)
	}

	offset := 12
	var (
		audioFormat uint16
		channels    uint16
		rate        uint32
		bits        uint16
		data        []byte
	)
	for offset+8 <= len(raw) {
		chunkID := string(raw[offset : offset+4])
		chunkSize := int(binary.LittleEndian.Uint32(raw[offset+4 : offset+8]))
		offset += 8
		if offset+chunkSize > len(raw) {
			return nil, 0, fmt.Errorf("truncated WAV chunk %q in %s", chunkID, path)
		}
		payload := raw[offset : offset+chunkSize]
		switch chunkID {
		case "fmt ":
			if chunkSize < 16 {
				return nil, 0, fmt.Errorf("fmt chunk too small in %s", path)
			}
			audioFormat = binary.LittleEndian.Uint16(payload[0:2])
			channels = binary.LittleEndian.Uint16(payload[2:4])
			rate = binary.LittleEndian.Uint32(payload[4:8])
			bits = binary.LittleEndian.Uint16(payload[14:16])
		case "data":
			data = payload
		}
		offset += chunkSize
		if chunkSize%2 == 1 {
			offset++ // word align
		}
	}
	if data == nil {
		return nil, 0, fmt.Errorf("WAV missing data chunk: %s", path)
	}
	if audioFormat != 1 {
		return nil, 0, fmt.Errorf("WAV not PCM (format=%d): %s", audioFormat, path)
	}
	if channels != 1 {
		return nil, 0, fmt.Errorf("WAV must be mono (channels=%d): %s", channels, path)
	}
	if bits != 16 {
		return nil, 0, fmt.Errorf("WAV must be 16-bit (bits=%d): %s", bits, path)
	}
	return data, int(rate), nil
}
