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

	delivery, err := rt.rescueOrchestrator.GenerateAndSynthesize(ctx, level, turnID, convCtx)
	if err != nil {
		h.logWarn(rt, "rescue_generate_failed",
			"B8 rescue ladder generation failed; no prompt sent",
			"session_id", session.SessionID,
			"level", level,
			"err", err,
		)
		return
	}
	frame := delivery.Frame

	// Text first, always. The learner is sitting in silence and the text is the
	// payload; the audio is how it lands. Sending them in one batch keeps the
	// rung's cost off the client's critical path either way, and guarantees that
	// a slow synthesis cannot delay the prompt.
	if err := rt.sendOutbound(ctx, conn, []ProviderOutbound{{Control: frame}}); err != nil {
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

	token := rt.beginRescueSpeech(frame.TurnID)
	spoken := false
	if delivery.Audio != nil {
		spoken = h.sendRescueAudio(ctx, conn, rt, *delivery.Audio, frame.TurnID, token)
	}

	h.logger.Info("B8 rescue ladder emitted",
		"session_id", session.SessionID,
		"turn_id", frame.TurnID,
		"level", frame.Level,
		"text_length", len(frame.Text),
		"spoken", spoken,
		"stage", "b8_rescue",
	)
}

// sendRescueAudio speaks one rung and reports whether it finished uninterrupted.
//
// The rung goes out **at the pace it is spoken**, not as fast as the socket
// accepts it — and that pacing now lives in the utterance writer, along with the
// framing and the end-of-stream rule, because the provider needs the same three
// things for the AI's own speech. Two copies of them had already drifted:
// dev-echo numbered frames from 0 and volc from 1.
//
// The write path is sendWithoutRescueState: the ladder's audio must not be fed
// back into the silence detector as if it were the AI taking a turn, or every
// rung would reset the window that decides the next one.
func (h *Handler) sendRescueAudio(
	ctx context.Context,
	conn *websocket.Conn,
	rt *sessionRuntime,
	audio RescueAudio,
	turnID string,
	token uint64,
) bool {
	writer, err := beginUtterance(Utterance{
		ID:      turnID,
		Kind:    UtteranceLadder,
		Format:  AudioFormat{SampleRate: audio.SampleRate, Channels: 1, BitsPerSample: 16},
		VoiceID: audio.VoiceID,
		Codec:   audio.Codec,
	}, func(outbound []ProviderOutbound) error {
		return rt.sendWithoutRescueState(ctx, conn, outbound)
	}, rt.audioSeq, func() bool {
		return rt.rescueSpeechCurrent(token)
	})
	if err != nil {
		h.logRescueAudioFailure(turnID, err)
		return false
	}
	if err := writer.Audio(audio.PCM); err != nil {
		h.logRescueAudioFailure(turnID, err)
		return false
	}
	// Stop here if the read loop already ended this stream because the learner
	// spoke: they are answering the ladder, which is what it was for.
	if !rt.rescueSpeechCurrent(token) {
		return false
	}
	if err := writer.End(); err != nil {
		h.logRescueAudioFailure(turnID, err)
		return false
	}
	rt.finishRescueSpeech(token)
	return true
}

// logRescueAudioFailure records a failed audio write. Not deduplicated: the text
// ladder has already reached the learner, so this is one lost enhancement rather
// than a cascade on the session's hot path.
func (h *Handler) logRescueAudioFailure(turnID string, err error) {
	h.logger.Warn("B8 rescue ladder audio write failed",
		"turn_id", turnID,
		"err", err,
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
	// Every outcome closes the turn, including the ones rescue ignores: a
	// timeout ended the turn as surely as an answer did.
	rt.applyTurnEvent(EvAIEnd)
	if !turnOutcomeSpoke(outcome) {
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

// applyTurnEvent advances the turn and reports a refused event.
//
// The reporting is the point of the rejection counter: an event the client sent
// in an order the machine does not allow is a client bug, and the alternative to
// counting it is a packet capture. It is deduplicated through the handler's warn
// path because a client that does this once per turn does it on every turn.
func (rt *sessionRuntime) applyTurnEvent(ev TurnEvent) {
	if !rt.turn.Apply(ev) {
		rt.reportTurnRejection(ev)
	}
}

func (rt *sessionRuntime) reportTurnRejection(ev TurnEvent) {
	if rt.warn == nil {
		return
	}
	rt.warn("turn_transition_rejected",
		"turn state machine refused an event; the client's ordering is unexpected",
		"session_id", rt.sessionID,
		"event", ev.String(),
		"state", rt.turn.State().String(),
	)
}

// noteUserSpeechStart restarts the ladder for a fresh attempt and pushes the
// user's previous AI message into the history.
func (rt *sessionRuntime) noteUserSpeechStart() {
	// The state machine is told first, and unconditionally: a turn's ordering is
	// session truth, not a rescue concern. Rescue is one consumer of it.
	rt.applyTurnEvent(EvUserSpeechStart)
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
func (h *Handler) noteUserSpeechEnd(ctx context.Context, conn *websocket.Conn, rt *sessionRuntime, data []byte) {
	// The turn advances before anything rescue-shaped happens, and even when
	// rescue is switched off: which turn we are in and what it is called is
	// session truth that every consumer reads, not a property of one feature.
	var end voiceproto.UserSpeechEnd
	parseErr := json.Unmarshal(data, &end)
	if parseErr == nil {
		if !rt.turn.ApplySpeechEnd(end.TurnID) {
			rt.reportTurnRejection(EvUserSpeechEnd)
		}
	}
	if !rt.rescueEnabled() {
		return
	}
	if parseErr != nil {
		rt.silenceDetector.OnUserSpeechEnd(rt.clock(), true)
		return
	}
	text := strings.TrimSpace(end.Text)
	complete := text == ""
	if !complete {
		complete = !incompleteDetector.IsIncomplete(text)
	}
	rt.silenceDetector.OnUserSpeechEnd(rt.clock(), complete)
	// The learner is speaking again, which is what the ladder was for. Stop any
	// rung still being spoken so the client is not talked over.
	rt.stopRescueSpeech(ctx, conn, func(key, msg string, args ...any) {
		h.logWarn(rt, key, msg, args...)
	})

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
	rt.applyTurnEvent(EvTurnAbort)
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

// beginRescueSpeech marks a rung as the one currently being spoken and returns
// the token that says whether it still is.
//
// The token exists because two goroutines touch one rung: the ladder goroutine
// writes its frames, and the read loop stops it the moment the learner speaks.
// A counter rather than a bool because the stop must also cancel a *pending*
// start — a bool would let the next rung inherit the previous one's liveness.
func (rt *sessionRuntime) beginRescueSpeech(turnID string) uint64 {
	rt.rescueSpeechMu.Lock()
	defer rt.rescueSpeechMu.Unlock()
	rt.rescueSpeechGen++
	rt.rescueSpeechTurn = turnID
	return rt.rescueSpeechGen
}

// finishRescueSpeech releases the stream after the ladder ended it normally.
//
// It does not send the end frame — the writer already did, which is the point of
// having one owner for that: this call only clears the state that tells the read
// loop a rung is in progress. The token check keeps a late finisher from clearing
// a newer rung's state.
func (rt *sessionRuntime) finishRescueSpeech(token uint64) {
	rt.rescueSpeechMu.Lock()
	defer rt.rescueSpeechMu.Unlock()
	if rt.rescueSpeechGen != token {
		return
	}
	rt.rescueSpeechTurn = ""
}

// rescueSpeechCurrent reports whether token still owns the audio stream.
func (rt *sessionRuntime) rescueSpeechCurrent(token uint64) bool {
	rt.rescueSpeechMu.Lock()
	defer rt.rescueSpeechMu.Unlock()
	return rt.rescueSpeechGen == token
}

// stopRescueSpeech interrupts a rung that is still being spoken, if any.
//
// Called when the user starts talking. Sending the ai.tts.end is what actually
// stops playback: the client keeps playing a stream until it is ended, and an
// unterminated rung would talk over the answer the ladder just successfully
// prompted. A no-op when nothing is being spoken, so the caller does not have to
// know whether the ladder had audio.
// The state is read and bumped under a mutex so the stop is one atomic step:
// bumping the generation *before* the turn id is read is what guarantees the
// ladder goroutine cannot allocate a sequence from a rung it no longer owns.
func (rt *sessionRuntime) stopRescueSpeech(
	ctx context.Context,
	conn *websocket.Conn,
	warn func(key, msg string, args ...any),
) {
	rt.rescueSpeechMu.Lock()
	turnID := rt.rescueSpeechTurn
	if turnID == "" {
		rt.rescueSpeechMu.Unlock()
		return
	}
	rt.rescueSpeechGen++ // cancels the token the ladder goroutine holds
	rt.rescueSpeechTurn = ""
	rt.rescueSpeechMu.Unlock()

	// The end frame is sent from *this* goroutine, not by the writer: the user is
	// talking right now, and waiting for the ladder's goroutine to notice would
	// leave the client playing over them. The writer sees the cleared state
	// before its own End and stays silent, so the stream gets exactly one end.
	end := voiceproto.AITTSEnd{
		Type:             voiceproto.TypeAITTSEnd,
		TurnID:           turnID,
		CompletionStatus: "interrupted",
	}
	if err := rt.sendWithoutRescueState(ctx, conn, []ProviderOutbound{{Control: end}}); err != nil && warn != nil {
		// The learner has already started talking; a failed stop costs them a
		// second of talking over the ladder, not the session.
		warn("rescue_interrupt_failed",
			"B8 rescue ladder interrupt failed",
			"turn_id", turnID,
			"err", err,
		)
	}
}
