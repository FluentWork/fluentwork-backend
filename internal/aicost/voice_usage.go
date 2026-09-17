package aicost

import (
	"context"
	"strings"
)

// Voice task types. One row per *billable SKU*, not per modality — the
// distinction is what keeps the ledger from double counting (P1-5).
//
// The voice path bills as:
//
//   - voice.duplex — the realtime session. Its SKU bundles ASR, TTS and the
//     dialogue model into one stream; the gateway reports the audio seconds it
//     moved (51_ §3).
//   - voice.tts — standalone synthesis (B17). Bills by characters, which is why
//     the ledger grew a chars column rather than reusing tokens_in.
//
// Two items that look like gaps are deliberately *not* rows:
//
//   - voice.asr: today every ASR the server pays for arrives inside a duplex
//     session, and the client-side path (B13) runs on the device. A separate row
//     would bill the same audio twice. It becomes its own SKU the day a
//     standalone ASR call exists — at which point it records audio_sec.
//   - voice.hit_detect: B7 detection is local token-overlap scoring against the
//     learner's corpus (PRD §5.2.3). No vendor is called, so there is no
//     variable cost to attribute; the cost of the ASR text it reads is already
//     the duplex row.
const (
	// TaskTypeVoiceDuplex is the realtime voice session SKU.
	TaskTypeVoiceDuplex = "voice.duplex"
	// TaskTypeVoiceTTS is the standalone synthesis SKU.
	TaskTypeVoiceTTS = "voice.tts"
)

// TTSRecorder adapts the TTS module's accounting seam onto this ledger.
//
// It takes plain numbers rather than ledger types so the tts package does not
// depend on aicost; the mapping lives here, next to the units it maps.
type TTSRecorder struct {
	Svc *Service
}

// RecordTTSUsage writes one synthesis as usage-is-fact / price-pending, the same
// discipline as the duplex row (51_ §4.3): the characters are measured, the fen
// are not yet.
//
// AudioSec stays 0 on purpose. The streaming provider returns ogg_opus, so bytes
// do not convert to time without decoding, and the duration is not the billing
// unit — characters are. A guessed duration would be exactly the kind of
// authoritative-looking number that rule exists to keep out.
func (r TTSRecorder) RecordTTSUsage(ctx context.Context, voiceID string, chars int) error {
	if r.Svc == nil {
		return nil
	}
	chars = max(chars, 0)
	voiceID = strings.TrimSpace(voiceID)
	if voiceID == "" {
		voiceID = "default"
	}
	_, err := r.Svc.Record(ctx, RecordRequest{
		TaskType:      TaskTypeVoiceTTS,
		Model:         voiceID,
		Chars:         chars,
		CostMicroYuan: 0,
	})
	return err
}
