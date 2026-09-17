package voiceproto

// RescueLadder represents a rescue prompt sent to the user when they are silent for too long.
// This frame provides progressively more specific help (3 levels: skeleton, hint, complete).
//
// # Audio does not travel in this frame
//
// AudioURL stays empty, and that is the design rather than an unfinished part:
// when a rung is spoken, the audio arrives as an ai.tts.start / ai.tts.audio /
// ai.tts.end stream on the ladder's own turn_id, which is the channel the client
// already plays (docs/92 §3). The field is kept — removing a key from a frozen
// protocol is a client-visible change, and it costs nothing to leave a URL that
// is never set. DurationMS is likewise unset; the client gets the duration from
// ai.tts.end.
type RescueLadder struct {
	Type       string `json:"type"`        // "ai.rescue.ladder"
	TurnID     string `json:"turn_id"`     // Current turn identifier
	Level      int    `json:"level"`       // 1=skeleton, 2=hint, 3=complete
	Text       string `json:"text"`        // Rescue content (Level 1/3: English, Level 2: Chinese)
	AudioURL   string `json:"audio_url"`   // Reserved; the spoken rung arrives as ai.tts.*
	DurationMS int64  `json:"duration_ms"` // Reserved; see ai.tts.end
	TS         int64  `json:"ts"`          // Unix timestamp in milliseconds
}

const (
	// TypeRescueLadder is the frame type for rescue ladder prompts.
	TypeRescueLadder = "ai.rescue.ladder"

	// RescueLevelSkeleton is the rung that hands the user a sentence opening:
	// "I think the main risk is..."
	RescueLevelSkeleton = 1
	// RescueLevelHint is the meta-cognitive nudge, in Chinese, that names a way
	// to organise the answer: "先说结论"
	RescueLevelHint = 2
	// RescueLevelComplete is the worked example: one complete sentence the user
	// can either say or riff on.
	RescueLevelComplete = 3
)

// ValidRescueLevel checks if the level is valid (1, 2, or 3).
func ValidRescueLevel(level int) bool {
	return level >= RescueLevelSkeleton && level <= RescueLevelComplete
}
