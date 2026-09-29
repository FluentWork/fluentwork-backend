package reviewgen

import (
	"encoding/json"
	"strings"
)

// StuckFirstBlocks reorders a refine document so the blocks that came from a B8
// stuck point lead (PRD §7.2 D1: 「自动提炼 3-5 个话术块…**优先选自卡壳点**」).
//
// It exists because the ordering cannot be left to the prompt. The prompt does
// state the target (`order refine blocks so those derived from rescue_events
// first`), but a model's ordering is a suggestion, and the requirement is a
// product rule: the block a learner was *stuck on* is the one they most need to
// see first. The stuck points are on this side already — `Request.StuckEvents`
// carries each ladder's anchor — and every block carries `anchor_user_said`,
// which the prompt requires to be a transcript substring. So the order is
// **derivable**, and what is derivable should not be pleaded for.
//
// It is a **stable partition**, not a sort: blocks inside each group keep the
// order the model gave them, because that order is the model's own ranking and
// this function has no basis to override it.
//
// The matching is by anchor, on trimmed case-insensitive equality: the anchor is
// the same string on both sides (the gateway resolves it, the model quotes it
// back), and the only realistic drift is whitespace or case. A block whose
// anchor matches nothing is not stuck-sourced and keeps its relative position —
// this function never invents provenance. An **empty** stuck anchor matches
// nothing by construction: the silent path's events carry no anchor (§5.2.2 —
// the user never spoke, so the event produces no block), and treating "" as a
// key would pull anchorless blocks forward as if they had provenance.
//
// Degenerate inputs are passed through **unchanged**, never repaired: a refine
// document that does not parse, one with no `blocks`, one with fewer than two
// blocks, or one with no stuck anchors to sort by has nothing this function can
// do better than the model already did. Reordering is an improvement; dropping
// or rewriting is not.
func StuckFirstBlocks(refine json.RawMessage, stuckAnchors []string) json.RawMessage {
	wanted := make(map[string]struct{}, len(stuckAnchors))
	for _, anchor := range stuckAnchors {
		if key := anchorKey(anchor); key != "" {
			wanted[key] = struct{}{}
		}
	}
	if len(wanted) == 0 {
		return refine
	}

	var document map[string]json.RawMessage
	if err := json.Unmarshal(refine, &document); err != nil {
		return refine
	}
	rawBlocks, ok := document["blocks"]
	if !ok {
		return refine
	}
	var blocks []json.RawMessage
	if err := json.Unmarshal(rawBlocks, &blocks); err != nil || len(blocks) < 2 {
		return refine
	}

	stuck := make([]json.RawMessage, 0, len(blocks))
	rest := make([]json.RawMessage, 0, len(blocks))
	for _, block := range blocks {
		if _, hit := wanted[anchorKey(anchorOf(block))]; hit {
			stuck = append(stuck, block)
			continue
		}
		rest = append(rest, block)
	}
	// Nothing to change: either nothing was stuck-sourced, or everything was, and
	// in both cases the model's order is already the answer. Returning the input
	// bytes keeps this function a no-op on the documents it cannot improve.
	if len(stuck) == 0 || len(rest) == 0 {
		return refine
	}

	encoded, err := json.Marshal(append(stuck, rest...))
	if err != nil {
		return refine
	}
	document["blocks"] = encoded
	out, err := json.Marshal(document)
	if err != nil {
		return refine
	}
	return out
}

func anchorKey(anchor string) string {
	return strings.ToLower(strings.TrimSpace(anchor))
}

// anchorOf reads one block's anchor without disturbing the rest of its bytes:
// blocks travel as raw messages so that reordering can never drop a field the
// prompt will grow later.
func anchorOf(block json.RawMessage) string {
	var probe struct {
		AnchorUserSaid string `json:"anchor_user_said"`
	}
	if err := json.Unmarshal(block, &probe); err != nil {
		return ""
	}
	return probe.AnchorUserSaid
}
