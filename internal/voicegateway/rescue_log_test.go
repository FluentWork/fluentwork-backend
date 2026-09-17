package voicegateway

import "testing"

// A silent turn where the user never speaks must still produce an event — the
// 卡壳事件 of PRD §5.4.4 — but with no anchor, which is what tells refine not to
// invent a phrase block for it.
func TestRescueLog_SilentWithoutAnchor(t *testing.T) {
	var log rescueLog
	log.noteLadder("turn-1", 1, "I think the main risk is…")
	log.noteLadder("turn-1", 2, "先说结论，再说原因")
	log.closeEpisode()

	events := log.snapshot()
	if len(events) != 1 {
		t.Fatalf("events = %+v", events)
	}
	event := events[0]
	if event.path != rescuePathSilent || event.level != 2 {
		t.Fatalf("event = %+v", event)
	}
	if event.opened || event.anchor != "" {
		t.Fatalf("anchor must stay empty when the user never spoke: %+v", event)
	}
	if event.ladder != "先说结论，再说原因" {
		t.Fatalf("ladder = %q, want the last rung", event.ladder)
	}
}

// Rungs 1..3 are one stuck point, not three: the event carries the deepest rung
// and the level-3 expression that becomes the block's expression_en.
func TestRescueLog_RungsShareOneEpisode(t *testing.T) {
	var log rescueLog
	log.noteLadder("turn-1", 1, "I think the main risk is…")
	log.noteLadder("turn-1", 2, "先说结论")
	log.noteLadder("turn-1", 3, "The main risk I see is the migration window.")
	log.noteUserText("The main risk I see is the migration window.")
	log.closeEpisode()

	events := log.snapshot()
	if len(events) != 1 {
		t.Fatalf("want one episode for three rungs, got %+v", events)
	}
	if events[0].level != 3 || !events[0].opened {
		t.Fatalf("event = %+v", events[0])
	}
	if events[0].anchor != "The main risk I see is the migration window." {
		t.Fatalf("anchor = %q, want the first utterance after the ladder", events[0].anchor)
	}
}

// §5.2.2: the incomplete path anchors to the half-sentence the user got stuck
// on, and keeps it even if they say something else afterwards.
func TestRescueLog_IncompletePathKeepsHalfSentence(t *testing.T) {
	var log rescueLog
	log.noteIncompleteUtterance("I was going to say that the deploy is")
	log.noteLadder("turn-1", 1, "I was going to say that the deploy is…")
	log.noteUserText("Actually the deploy is blocked on the migration.")
	log.closeEpisode()

	events := log.snapshot()
	if len(events) != 1 {
		t.Fatalf("events = %+v", events)
	}
	if events[0].path != rescuePathIncomplete {
		t.Fatalf("path = %q, want incomplete", events[0].path)
	}
	if events[0].anchor != "I was going to say that the deploy is" {
		t.Fatalf("anchor = %q, want the half-sentence", events[0].anchor)
	}
	if !events[0].opened {
		t.Fatalf("an incomplete utterance means the user did speak")
	}
}

// An AI turn end closes the episode: rungs cannot span turns, and a later rung
// starts a new one with its own seq.
func TestRescueLog_CloseStartsANewEpisode(t *testing.T) {
	var log rescueLog
	log.noteLadder("turn-1", 1, "skeleton one")
	log.closeEpisode()
	log.noteLadder("turn-2", 1, "skeleton two")
	log.noteLadder("turn-2", 2, "hint two")

	events := log.snapshot()
	if len(events) != 2 {
		t.Fatalf("events = %+v", events)
	}
	if events[0].seq != 1 || events[1].seq != 2 {
		t.Fatalf("seqs = %d, %d", events[0].seq, events[1].seq)
	}
	if events[1].turnID != "turn-2" || events[1].level != 2 {
		t.Fatalf("second event = %+v", events[1])
	}
}

// A half-sentence the ladder never answered to is not a stuck point the product
// rescued: closing the window must drop it rather than leak it into the next
// episode's anchor.
func TestRescueLog_PendingIncompleteExpiresWithTheWindow(t *testing.T) {
	var log rescueLog
	log.noteIncompleteUtterance("half a thought")
	log.closeEpisode()
	log.noteLadder("turn-2", 1, "skeleton")

	events := log.snapshot()
	if len(events) != 1 {
		t.Fatalf("events = %+v", events)
	}
	if events[0].path != rescuePathSilent || events[0].anchor != "" {
		t.Fatalf("stale half-sentence leaked into the next episode: %+v", events[0])
	}
}

// snapshot seals whatever is still open, so a session that ends mid-ladder
// still reports the stuck point it was in.
func TestRescueLog_SnapshotSealsOpenEpisode(t *testing.T) {
	var log rescueLog
	log.noteLadder("turn-1", 1, "skeleton")
	if events := log.snapshot(); len(events) != 1 {
		t.Fatalf("events = %+v", events)
	}
	if events := log.snapshot(); len(events) != 1 {
		t.Fatalf("snapshot must not duplicate the sealed episode: %+v", events)
	}
}
