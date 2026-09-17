package voicegateway

import (
	"strings"
	"sync"
)

// Rescue path labels, mirrored on the wire so app-server never has to infer
// which trigger produced a ladder (PRD §5.4.1 / §5.2.2).
const (
	rescuePathIncomplete = "incomplete"
	rescuePathSilent     = "silent"
)

// rescueEpisode is one stuck point a ladder was sent for (PRD §5.4.4).
//
// One episode spans from its first rung to the next AI turn end. Rungs 1..3
// belong to the same hesitation: reporting them separately would ask refine for
// three phrase blocks instead of one.
type rescueEpisode struct {
	seq    int
	turnID string
	path   string
	// level is the deepest rung the ladder reached (PRD §5.4.4's "层数").
	level  int
	ladder string
	// anchor follows PRD §5.2.2: the half-sentence for the incomplete path, the
	// first utterance after the ladder for the silent path, empty when the user
	// never spoke — which is also the signal to produce no phrase block.
	anchor string
	opened bool
}

// rescueLog accumulates a session's stuck points for refine's second input.
//
// It is written from three goroutines (the frame loop, the silence poller and
// the turn collector), so it carries its own mutex rather than sharing
// rescueMu — nothing here reads the conversation context, and one lock for one
// job keeps the ordering trivial.
type rescueLog struct {
	mu       sync.Mutex
	episodes []rescueEpisode
	open     *rescueEpisode
	// pendingIncomplete holds the half-sentence of an utterance that stopped
	// mid-thought until the rung it belongs to arrives. The user stops talking
	// before the ladder fires, so the anchor is known before the episode is.
	pendingIncomplete string
	seq               int
}

// noteIncompleteUtterance remembers a half-sentence for the episode about to
// open. Only the first one is kept: §5.2.2 anchors the incomplete path to the
// utterance that actually got stuck.
func (l *rescueLog) noteIncompleteUtterance(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.pendingIncomplete == "" {
		l.pendingIncomplete = text
	}
}

// noteLadder records one rung. The first rung opens the episode; later rungs
// deepen it, and the last text is kept because level 3 carries the complete
// expression that becomes the block's expression_en.
func (l *rescueLog) noteLadder(turnID string, level int, text string) {
	text = strings.TrimSpace(text)
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.open == nil {
		l.seq++
		episode := &rescueEpisode{
			seq:    l.seq,
			turnID: strings.TrimSpace(turnID),
			path:   rescuePathSilent,
		}
		if l.pendingIncomplete != "" {
			episode.path = rescuePathIncomplete
			episode.anchor = l.pendingIncomplete
			episode.opened = true
			l.pendingIncomplete = ""
		}
		l.open = episode
	}
	if level > l.open.level {
		l.open.level = level
	}
	if text != "" {
		l.open.ladder = text
	}
}

// noteUserText resolves the silent path's anchor: the first thing the user
// manages to say after the ladder (§5.2.2). Callers pass whatever the ASR path
// resolved, because with B13's gate off the client frame carries no text.
func (l *rescueLog) noteUserText(text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		return
	}
	l.mu.Lock()
	defer l.mu.Unlock()
	if l.open == nil {
		return
	}
	l.open.opened = true
	if l.open.anchor == "" {
		l.open.anchor = text
	}
}

// closeEpisode ends the current episode at an AI turn boundary. Rungs cannot
// cross that boundary: a new AI turn opens a new silence window (see
// SilenceDetector.OnAISpeechEnd), so a new stuck point starts there.
func (l *rescueLog) closeEpisode() {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sealLocked()
	// A half-sentence whose ladder never fired is not a stuck point the product
	// rescued — the AI answered instead.
	l.pendingIncomplete = ""
}

// snapshot seals the open episode and returns every episode in order.
func (l *rescueLog) snapshot() []rescueEpisode {
	l.mu.Lock()
	defer l.mu.Unlock()
	l.sealLocked()
	return append([]rescueEpisode(nil), l.episodes...)
}

func (l *rescueLog) sealLocked() {
	if l.open == nil {
		return
	}
	if l.open.level < 1 {
		// A rung always carries a level; guard anyway so a malformed frame
		// cannot put an unusable event in front of refine.
		l.open.level = 1
	}
	l.episodes = append(l.episodes, *l.open)
	l.open = nil
}
