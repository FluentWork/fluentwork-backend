package voiceproto

// RescueLadder represents a rescue prompt sent to the user when they are silent for too long.
// This frame provides progressively more specific help (3 levels: skeleton, hint, complete).
type RescueLadder struct {
	Type       string `json:"type"`        // "ai.rescue.ladder"
	TurnID     string `json:"turn_id"`     // Current turn identifier
	Level      int    `json:"level"`       // 1=skeleton, 2=hint, 3=complete
	Text       string `json:"text"`        // Rescue content (Level 1/3: English, Level 2: Chinese)
	AudioURL   string `json:"audio_url"`   // TTS audio URL
	DurationMS int64  `json:"duration_ms"` // Audio duration in milliseconds
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
