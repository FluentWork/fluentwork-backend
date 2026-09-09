package voicegateway

import (
	"fmt"
	"strings"
)

// canonicalTurnID prefers the client-supplied id (iOS: "turn-N").
// When the client omits it, use the same namespace — never "volc-turn-*"
// or "dev-echo-turn", which cannot join iOS tracker events.
func canonicalTurnID(clientID string, seq int) string {
	if t := strings.TrimSpace(clientID); t != "" {
		return t
	}
	if seq <= 0 {
		return ""
	}
	return fmt.Sprintf("turn-%d", seq)
}
