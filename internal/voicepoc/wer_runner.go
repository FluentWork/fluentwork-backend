package voicepoc

import (
	"context"
	"fmt"
	"time"
)

// TranscribeFixture sends one WAV through a real duplex turn and returns what
// the ASR heard.
//
// Deliberately **not** SmokeDuplexASR, which returns an error when the
// transcript is empty ("V1 FAIL"). For WER an empty transcript is a
// *measurement* — every reference word deleted — and the samples most likely to
// produce one are the hardest ones. A runner that errors on those would drop
// exactly the worst cases and report a flattering average.
//
// The WAV must be 16 kHz mono PCM16; the duplex protocol accepts nothing else.
func TranscribeFixture(ctx context.Context, cfg DuplexConfig, wavPath string) (string, error) {
	pcm, rate, err := LoadWAVPCM16LE(wavPath)
	if err != nil {
		return "", err
	}
	if rate != 16000 {
		return "", fmt.Errorf("%s: sample rate %d != 16000; WER samples must be 16 kHz mono PCM16", wavPath, rate)
	}

	// The assistant still answers — the protocol has no ASR-only mode — but its
	// reply is irrelevant here, so keep it short and out of the way.
	cfg.Instructions = firstNonEmpty(cfg.Instructions,
		"Reply with a single short word. Do not ask questions.")

	session, err := OpenDuplex(ctx, cfg)
	if err != nil {
		return "", err
	}
	defer func() { _ = session.Close(ctx) }()

	turn, err := session.SendUserPCMAndWait(ctx, pcm, 30*time.Second)
	if err != nil {
		return "", err
	}
	return turn.Transcript, nil
}
