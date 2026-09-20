package voicegateway

import (
	"strings"
)

// canonicalTurnID prefers the client-supplied id (iOS: "turn-N").
// When the client omits it, fall back to session_id so all frames for the same
// turn carry the same id (badge, ai.text.delta, ai.tts.start, ai.turn.end).
func canonicalTurnID(clientID string, sessionID string) string {
	if t := strings.TrimSpace(clientID); t != "" {
		return t
	}
	return sessionID
}
