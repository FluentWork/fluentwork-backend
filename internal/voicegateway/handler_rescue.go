package voicegateway

import (
	"context"
	"encoding/json"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/FluentWork/fluentwork-backend/internal/conversation"
	"github.com/FluentWork/fluentwork-backend/internal/voiceproto"
)

// defaultRescueTick is how often the B8 silence detector is polled.
//
// The PRD's rungs are three seconds apart, so this only has to be fine enough
// that a rung is not visibly late. 500ms costs one timer wakeup per session per
// second and puts a rung at most 0.5s after its threshold — well inside the
// tolerance of a prompt whose whole purpose is "you have been quiet a while".
const defaultRescueTick = 500 * time.Millisecond

// rescueRecentTurns is how many turns of history the level-3 generator is given.
// It only needs enough to know what is being discussed; the full transcript is
// the review pipeline's business.
const rescueRecentTurns = 4

// incompleteDetector judges whether the user's utterance was a finished
// sentence. It holds no state and reads no clock, so one instance serves every
// session — unlike the silence detector, which is deliberately per session.
var incompleteDetector = conversation.NewIncompleteDetector()

// rescueDetectorForSession returns the silence detector a new session should use.
//
// It clones the wired template's thresholds rather than handing over the
// template itself: a detector carries a live window, and two concurrent sessions
// sharing one window would consume each other's rungs. See SetRescueComponents.
func (h *Handler) rescueDetectorForSession() *SilenceDetector {
	if h.silenceDetector == nil {
		return nil
	}
	return NewSilenceDetectorWithThresholds(h.silenceDetector.Thresholds())
}

// startRescueLoop runs this session's silence poller.
//
// A no-op unless both rescue components were wired, which is also the feature's
// off switch: an unwired gateway behaves exactly as it did before B8 existed.
func (h *Handler) startRescueLoop(
	ctx context.Context,
	conn *websocket.Conn,
	rt *sessionRuntime,
	session ConsumedTicket,
) {
	if !rt.rescueEnabled() {
		return
	}
	tick := rt.rescueTick
	if tick <= 0 {
		tick = defaultRescueTick
	}
	rt.rescueStop = make(chan struct{})
	rt.rescueWG.Add(1)
	go func() {
		defer rt.rescueWG.Done()
		ticker := time.NewTicker(tick)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return
			case <-rt.rescueStop:
				return
			case <-ticker.C:
				h.checkRescue(ctx, conn, rt, session)
			}
		}
	}()
}

// checkRescue asks the detector for the next rung and, if there is one, starts
// generating it.
//
// Only the poller goroutine calls this, which is what makes rescueInFlight a
// busy flag rather than a lock. The order below is load-bearing: the busy check
// comes **before** CheckSilence, because CheckSilence consumes the rung it
// returns. Checking afterwards would drop a rung every time a generation
// overlapped a tick — and overlap is expected, since a slow model can take
// longer than the 3s between rungs.
func (h *Handler) checkRescue(
	ctx context.Context,
	conn *websocket.Conn,
	rt *sessionRuntime,
	session ConsumedTicket,
) {
	if !rt.rescueEnabled() || rt.rescueInFlight.Load() {
		return
	}

	shouldRescue, level := rt.silenceDetector.CheckSilence(rt.clock())
	if !shouldRescue {
		return
	}

	rt.rescueInFlight.Store(true)
	rt.rescueWG.Add(1)
	go func() {
		defer rt.rescueWG.Done()
		defer rt.rescueInFlight.Store(false)
		h.emitRescue(ctx, conn, rt, session, level)
	}()
}

// emitRescue generates one rung and writes it to the client.
//
// A failed ladder is logged and dropped, never turned into an error frame: the
// user is silent, possibly anxious, and an error banner is a worse outcome than
// no prompt at all. iOS is told nothing because there is nothing for it to do.
func (h *Handler) emitRescue(
	ctx context.Context,
	conn *websocket.Conn,
	rt *sessionRuntime,
	session ConsumedTicket,
	level int,
) {
	convCtx, turnID := rt.rescueSnapshot(session)

	frame, err := rt.rescueOrchestrator.GenerateAndSynthesize(ctx, level, turnID, convCtx)
	if err != nil {
		h.logWarn(rt, "rescue_generate_failed",
			"B8 rescue ladder generation failed; no prompt sent",
			"session_id", session.SessionID,
			"level", level,
			"err", err,
		)
		return
	}

	if err := rt.sendJSON(ctx, conn, frame); err != nil {
		h.logWarn(rt, "rescue_send_failed",
			"B8 rescue ladder write failed",
			"session_id", session.SessionID,
			"level", level,
			"err", err,
		)
		return
	}
	// Only a delivered rung counts: a failed generation was never a prompt the
	// user could have been stuck on.
	rt.rescueLog.noteLadder(frame.TurnID, frame.Level, frame.Text)

	h.logger.Info("B8 rescue ladder emitted",
		"session_id", session.SessionID,
		"turn_id", frame.TurnID,
		"level", frame.Level,
		"text_length", len(frame.Text),
		"has_audio", frame.AudioURL != "",
		"stage", "b8_rescue",
	)
}

// noteProviderOutbound feeds the AI's own output into the rescue state.
//
// ai.tts.end is what opens a silence window, and it is the right marker because
// it is the protocol's own statement that the AI has stopped talking. ai.turn.end
// is accepted as well: it means the same thing, arrives at the same moment, and
// is the only marker providers without a TTS path (mock, dev-echo) ever send —
// without it those providers would never rescue at all.
func (rt *sessionRuntime) noteProviderOutbound(outbound []ProviderOutbound) {
	if !rt.rescueEnabled() {
		return
	}
	for _, item := range outbound {
		if item.Control == nil {
			continue
		}
		switch typed := item.Control.(type) {
		case voiceproto.AITextDelta:
			if typed.Type == voiceproto.TypeAITextDelta {
				rt.appendAIText(typed.Text, typed.TurnID)
			}
		case voiceproto.AITTSEnd:
			if typed.Type == voiceproto.TypeAITTSEnd {
				rt.noteAITurnEnd(typed.TurnID, "")
			}
		case voiceproto.AITurnEnd:
			if typed.Type == voiceproto.TypeAITurnEnd {
				rt.noteAITurnEnd(typed.TurnID, typed.Outcome)
			}
		}
	}
}

// noteAITurnEnd opens the silence window and remembers which turn it belongs to.
//
// A turn that ended in timeout or error asked the user nothing, so there is
// nothing for them to be stuck on: opening a window there would have the gateway
// volunteer a skeleton for a conversation that never happened. That is not
// hypothetical — B15's 30s turn timeout ends a turn with exactly that outcome,
// and its 30s deadline arrives *after* the ladder has already spent its three
// rungs, so without this guard an unanswered turn is re-nagged from 33s onward
// by a feature whose whole purpose is to stop nagging.
func (rt *sessionRuntime) noteAITurnEnd(turnID, outcome string) {
	switch outcome {
	case voiceproto.TurnOutcomeTimeout, voiceproto.TurnOutcomeError:
		return
	}
	if tr := strings.TrimSpace(turnID); tr != "" {
		rt.rescueMu.Lock()
		rt.rescueTurnID = tr
		rt.rescueMu.Unlock()
	}
	// A new AI turn opens a new silence window, so the previous stuck point is
	// complete — rungs cannot span turns.
	rt.rescueLog.closeEpisode()
	rt.silenceDetector.OnAISpeechEnd(rt.clock())
}

// appendAIText accumulates the AI's current turn, so the generator can be told
// what the user has been asked to answer.
func (rt *sessionRuntime) appendAIText(text, turnID string) {
	if text == "" && strings.TrimSpace(turnID) == "" {
		return
	}
	rt.rescueMu.Lock()
	defer rt.rescueMu.Unlock()
	rt.convCtx.LastAIMessage += text
	if tr := strings.TrimSpace(turnID); tr != "" {
		rt.rescueTurnID = tr
	}
}

// noteScenario records what the practice is about. Called once per session.start;
// a second session.start on the same connection replaces it, which is what a
// client that reopened with a different scene would want.
func (rt *sessionRuntime) noteScenario(sceneType, materialID string) {
	if !rt.rescueEnabled() {
		return
	}
	scene := strings.TrimSpace(sceneType)
	if scene == "" {
		scene = strings.TrimSpace(materialID)
	}
	rt.rescueMu.Lock()
	rt.convCtx.ScenarioContext = scene
	rt.rescueMu.Unlock()
}

// noteUserSpeechStart restarts the ladder for a fresh attempt and pushes the
// user's previous AI message into the history.
func (rt *sessionRuntime) noteUserSpeechStart() {
	if !rt.rescueEnabled() {
		return
	}
	rt.silenceDetector.OnUserSpeechStart(rt.clock())
	rt.sealAITurn()
}

// noteUserSpeechEnd judges the utterance and tells the detector whether the user
// actually took the floor.
//
// Judgment only happens when there is text to judge. With B13's gate off — the
// default — the transcript is the provider's, not the client's, so this frame
// carries no text and the gateway has nothing to score. An unjudgeable utterance
// counts as complete: guessing "incomplete" would leave a ladder armed against a
// user who did answer, and a spurious skeleton prompt is a worse failure than a
// missing one.
func (rt *sessionRuntime) noteUserSpeechEnd(data []byte) {
	if !rt.rescueEnabled() {
		return
	}
	var end voiceproto.UserSpeechEnd
	if err := json.Unmarshal(data, &end); err != nil {
		rt.silenceDetector.OnUserSpeechEnd(rt.clock(), true)
		return
	}
	text := strings.TrimSpace(end.Text)
	complete := text == ""
	if !complete {
		complete = !incompleteDetector.IsIncomplete(text)
	}
	rt.silenceDetector.OnUserSpeechEnd(rt.clock(), complete)
	if !complete {
		// §5.2.2: the half-sentence is the incomplete path's anchor. Whether a
		// ladder actually follows is the poller's decision, so this is held
		// until a rung opens an episode.
		rt.rescueLog.noteIncompleteUtterance(text)
	}
	rt.appendRecentTurn("user", text)
}

// noteUserUtteranceText feeds the user's resolved utterance to the rescue log,
// which uses the first one after a ladder as the silent path's anchor (§5.2.2).
func (rt *sessionRuntime) noteUserUtteranceText(text string) {
	if !rt.rescueEnabled() {
		return
	}
	rt.rescueLog.noteUserText(text)
}

// snapshotRescueEvents returns this session's stuck points for the end report.
func (rt *sessionRuntime) snapshotRescueEvents() []EndRescueEvent {
	episodes := rt.rescueLog.snapshot()
	if len(episodes) == 0 {
		return nil
	}
	out := make([]EndRescueEvent, 0, len(episodes))
	for _, episode := range episodes {
		out = append(out, EndRescueEvent{
			Seq:        episode.seq,
			TurnID:     episode.turnID,
			Level:      episode.level,
			Path:       episode.path,
			Ladder:     episode.ladder,
			UserOpened: episode.opened,
			Anchor:     episode.anchor,
		})
	}
	return out
}

// noteTurnAbort handles an abandoned recording: the user stopped talking without
// finishing, so the ladder stays armed but rescue is no longer suspended.
func (rt *sessionRuntime) noteTurnAbort() {
	if !rt.rescueEnabled() {
		return
	}
	rt.silenceDetector.OnUserSpeechEnd(rt.clock(), false)
}

// sealAITurn moves the accumulated AI text into the history. Called when the
// user starts a new turn, which is the first moment we know the AI has finished
// being quoted — the rescue itself needs LastAIMessage to still be populated
// while the user is stuck, so it cannot be cleared at the AI's turn end.
func (rt *sessionRuntime) sealAITurn() {
	rt.rescueMu.Lock()
	defer rt.rescueMu.Unlock()
	if text := strings.TrimSpace(rt.convCtx.LastAIMessage); text != "" {
		rt.convCtx.RecentTurns = trimTurns(rt.convCtx.RecentTurns, conversation.Turn{
			Speaker: "ai",
			Content: text,
		})
	}
	rt.convCtx.LastAIMessage = ""
}

// appendRecentTurn adds a turn to the history and trims it.
func (rt *sessionRuntime) appendRecentTurn(speaker, content string) {
	content = strings.TrimSpace(content)
	if content == "" {
		return
	}
	rt.rescueMu.Lock()
	defer rt.rescueMu.Unlock()
	rt.convCtx.RecentTurns = trimTurns(rt.convCtx.RecentTurns, conversation.Turn{
		Speaker: speaker,
		Content: content,
	})
}

// trimTurns appends and trims, keeping the newest rescueRecentTurns entries.
func trimTurns(turns []conversation.Turn, turn conversation.Turn) []conversation.Turn {
	turns = append(turns, turn)
	if len(turns) > rescueRecentTurns {
		turns = turns[len(turns)-rescueRecentTurns:]
	}
	return turns
}

// rescueSnapshot freezes what the generator should be told, and which turn the
// ladder belongs to.
//
// The turn id is the AI's, because the rescue fires while the user's next turn
// does not exist yet — user.speech.start carries no turn_id and the user has not
// spoken. Falling back to the session id keeps the field non-empty for a client
// that keys its bubbles on it.
func (rt *sessionRuntime) rescueSnapshot(session ConsumedTicket) (conversation.ConversationContext, string) {
	rt.rescueMu.Lock()
	defer rt.rescueMu.Unlock()
	conv := rt.convCtx
	conv.RecentTurns = append([]conversation.Turn(nil), rt.convCtx.RecentTurns...)
	turnID := strings.TrimSpace(rt.rescueTurnID)
	if turnID == "" {
		turnID = session.SessionID
	}
	// Attribution travels with the request: the ladder's model call is billed
	// like every other one, and an unattributed row is a row nobody can act on.
	conv.SessionID = session.SessionID
	conv.UserID = session.UserID
	conv.TurnID = turnID
	return conv, turnID
}
