package voicegateway

import (
	"context"
	"encoding/json"
	"strings"

	"github.com/coder/websocket"

	"github.com/FluentWork/fluentwork-backend/internal/voiceproto"
)

// Control frames: the C→S half of the protocol. One handler per frame type, and
// a table saying what happens when the provider call behind it fails.
//
// The table exists because that decision used to be made inside each branch, and
// only one of the four was ever written down as a decision. The set is the thing
// worth seeing: "which frames fail silently, and why" is a question about the
// product, and it was answerable only by reading every branch (docs/94_ F6).

// controlOutcome is what a control frame's handler produced.
//
// The three cases are distinct and the distinction matters: "I handled it" and
// "this frame is not mine" both end the dispatch, but only the second should
// fall through to the unknown-frame path.
type controlOutcome int

const (
	// controlHandled: this handler owns the frame type, whatever it did with it.
	controlHandled controlOutcome = iota
	// controlNotMine: not this handler's frame type.
	controlNotMine
)

// providerErrorStrategy is what to tell the client when forwarding a frame to
// the provider fails.
type providerErrorStrategy struct {
	// code is the error frame's code. Empty means send no error frame at all.
	code string
	// silentBecause records why a frame sends nothing. Empty when code is set.
	//
	// It is required for silence rather than optional: silence is the choice that
	// needs justifying, because the failure it hides is a frame the client thinks
	// succeeded.
	silentBecause string
}

// announces is the strategy for failures the client should hear about.
func announces(code string) providerErrorStrategy {
	return providerErrorStrategy{code: code}
}

// silent is the strategy for failures the client must not hear about.
func silent(because string) providerErrorStrategy {
	return providerErrorStrategy{silentBecause: because}
}

// providerErrorStrategies maps each frame the gateway forwards to the provider
// onto its failure policy.
//
// The rule behind it: **announce only when the user is waiting for it.** Every
// frame here had its strategy chosen against that rule, and the one deviation is
// stated rather than implied.
var providerErrorStrategies = map[string]providerErrorStrategy{
	voiceproto.TypeUserSpeechStart: announces("provider_control_failed"),
	// The learner has already stopped; nothing is waiting on this frame. The
	// provider's failure is logged and the session continues.
	voiceproto.TypeUserSpeechEnd: announces("provider_control_failed"),
	voiceproto.TypeInterrupt:     announces("provider_interrupt_failed"),
	// Not announced, and this one is deliberate: an error frame here maps to
	// iOS `.failed`, which kills the very session this frame exists to keep
	// alive. The user stopped talking — they are not waiting for an answer to
	// the abort frame.
	voiceproto.TypeClientTurnAbort: silent("an error frame maps to iOS .failed and would kill the session the abort exists to keep alive"),
}

// ProviderErrorPolicy is one row of the failure policy, exported so a test can
// assert the table's shape without the table being reachable for mutation.
type ProviderErrorPolicy struct {
	// Code is the error frame's code; empty means nothing is sent.
	Code string
	// SilentBecause says why nothing is sent, when Code is empty.
	SilentBecause string
}

// ProviderErrorPolicies returns the failure policy for every frame the gateway
// forwards to the provider.
//
// Exported for the test that asks each frame type to have chosen: either a code
// to send, or a reason not to. The policy is the deliverable here, so it is worth
// being assertable from outside the package that owns the dispatch.
func ProviderErrorPolicies() map[string]ProviderErrorPolicy {
	out := make(map[string]ProviderErrorPolicy, len(providerErrorStrategies))
	for frameType, strategy := range providerErrorStrategies {
		out[frameType] = ProviderErrorPolicy{Code: strategy.code, SilentBecause: strategy.silentBecause}
	}
	return out
}

// HandleControl dispatches one text frame from the client loop.
func (h *Handler) HandleControl(
	ctx context.Context,
	conn *websocket.Conn,
	session ConsumedTicket,
	data []byte,
	rt *sessionRuntime,
) error {
	frameType, err := voiceproto.DecodeType(data)
	if err != nil {
		return h.invalidFrame(ctx, conn, err.Error())
	}
	handlers := []func(context.Context, *websocket.Conn, ConsumedTicket, []byte, *sessionRuntime) (controlOutcome, error){
		h.controlKeepalive,
		h.controlSessionStart,
		h.controlUserSpeech,
		h.controlTurnAbort,
		h.controlRescue,
		h.controlInterrupt,
		h.controlSessionEnd,
		h.controlAuth,
	}
	for _, handle := range handlers {
		outcome, err := handle(ctx, conn, session, data, rt)
		if err == nil && outcome == controlNotMine {
			continue
		}
		return err
	}
	return h.unknownFrame(ctx, conn, session, rt, frameType)
}

// invalidFrame answers a frame the gateway could not parse or accept.
func (h *Handler) invalidFrame(ctx context.Context, conn *websocket.Conn, message string) error {
	return rtSendError(ctx, conn, nil, "invalid_frame", message)
}

// rtSendError writes an error frame. rt may be nil when the runtime is not the
// subject of the failure.
func rtSendError(ctx context.Context, conn *websocket.Conn, rt *sessionRuntime, code, message string) error {
	frame := voiceproto.ErrorFrame{
		Type:    voiceproto.TypeError,
		Code:    code,
		Message: message,
	}
	if rt == nil {
		// Only reached before the runtime exists (a malformed first frame).
		return writeJSON(ctx, conn, frame)
	}
	return rt.sendJSON(ctx, conn, frame)
}

// forwardToProvider passes a frame to the provider and applies the frame type's
// failure policy from providerErrorStrategies.
//
// This is the one place that decides whether a provider failure is announced,
// so a frame type cannot pick a policy implicitly by being handled elsewhere.
func (h *Handler) forwardToProvider(
	ctx context.Context,
	conn *websocket.Conn,
	session ConsumedTicket,
	rt *sessionRuntime,
	frameType string,
	data []byte,
) error {
	outbound, err := rt.provider.HandleClientControl(ctx, frameType, data)
	if err != nil {
		h.logger.Warn("provider control forward failed",
			"session_id", session.SessionID,
			"type", frameType,
			"err", err,
		)
		strategy, known := providerErrorStrategies[frameType]
		if !known {
			// A frame that reaches the provider without a policy is a bug in
			// this package, not a client problem: it means a handler was added
			// without saying how its failures are reported. Announcing is the
			// safe default — silence would hide it.
			return rtSendError(ctx, conn, rt, "provider_control_failed", err.Error())
		}
		if strategy.code == "" {
			return nil
		}
		return rtSendError(ctx, conn, rt, strategy.code, err.Error())
	}
	return rt.sendOutbound(ctx, conn, outbound)
}

// started reports whether a provider session is open for this connection.
func started(rt *sessionRuntime) bool {
	return rt.started && rt.provider != nil
}

// controlKeepalive answers ping.
func (h *Handler) controlKeepalive(
	ctx context.Context,
	conn *websocket.Conn,
	_ ConsumedTicket,
	data []byte,
	rt *sessionRuntime,
) (controlOutcome, error) {
	frameType, err := voiceproto.DecodeType(data)
	if err != nil || frameType != voiceproto.TypePing {
		return controlNotMine, nil
	}
	var ping voiceproto.Ping
	if err := json.Unmarshal(data, &ping); err != nil {
		return controlHandled, h.invalidFrame(ctx, conn, err.Error())
	}
	ts := ping.TS
	if ts == 0 {
		ts = h.now().UnixMilli()
	}
	return controlHandled, rt.sendJSON(ctx, conn, voiceproto.Pong{Type: voiceproto.TypePong, TS: ts})
}

// controlSessionStart opens the session and the provider, once.
func (h *Handler) controlSessionStart(
	ctx context.Context,
	conn *websocket.Conn,
	session ConsumedTicket,
	data []byte,
	rt *sessionRuntime,
) (controlOutcome, error) {
	frameType, err := voiceproto.DecodeType(data)
	if err != nil || frameType != voiceproto.TypeSessionStart {
		return controlNotMine, nil
	}
	var start voiceproto.SessionStart
	if err := json.Unmarshal(data, &start); err != nil {
		return controlHandled, h.invalidFrame(ctx, conn, err.Error())
	}
	if !rt.started {
		if err := h.openSession(ctx, conn, session, rt); err != nil {
			return controlHandled, err
		}
	}
	h.logger.Info("session.start accepted",
		"session_id", session.SessionID,
		"user_id", session.UserID,
		"stage", "orchestration",
	)
	rt.lastStart = &start
	// B8: what the rescue generator is told the practice is about. SceneType is
	// the only scene information this frame carries, and MaterialID stands in
	// when a client omits it — a ladder generated against no context at all is
	// generic encouragement, which is the failure mode the level-3 example
	// exists to avoid.
	rt.noteScenario(start.SceneType, start.MaterialID)
	// Resolved once, before the first Start, so the reopened path replays the
	// same context (see rt.continuation). A refusal is not fatal: the session
	// opens without the tail, which is what it would have done before this
	// existed. Failing the whole session because a nice-to-have lookup missed
	// would trade a small loss for a total one.
	if previous := strings.TrimSpace(start.ContinueFromSessionID); previous != "" && h.lifecycle != nil {
		turns, ctxErr := h.lifecycle.ContinuationContext(ctx, session.SessionID, previous, 0)
		switch {
		case ctxErr != nil:
			h.logger.Warn("continuation context unavailable; opening without it",
				"session_id", session.SessionID,
				"continue_from_session_id", previous,
				"err", ctxErr,
			)
		default:
			rt.continuation = turns
			h.logger.Info("continuation context resolved",
				"session_id", session.SessionID,
				"continue_from_session_id", previous,
				"turns", len(turns),
				"stage", "orchestration",
			)
		}
	}
	outbound, err := rt.provider.Start(ctx, start, rt.continuation)
	if err != nil {
		h.logger.Warn("provider start failed", "session_id", session.SessionID, "err", err)
		return controlHandled, rtSendError(ctx, conn, rt, "provider_start_failed", err.Error())
	}
	return controlHandled, rt.sendOutbound(ctx, conn, outbound)
}

// openSession activates the session in app-server and opens a provider.
func (h *Handler) openSession(ctx context.Context, conn *websocket.Conn, session ConsumedTicket, rt *sessionRuntime) error {
	if h.lifecycle != nil {
		if err := h.lifecycle.Activate(ctx, session.SessionID); err != nil {
			h.logger.Warn("session activate failed", "session_id", session.SessionID, "err", err)
			return rtSendError(ctx, conn, rt, "activate_failed", err.Error())
		}
	}
	provider, err := h.provider.Open(ctx, session, rt.audioSeq, rt.turnRefs)
	if err != nil {
		h.logger.Warn("provider open failed", "session_id", session.SessionID, "err", err)
		return rtSendError(ctx, conn, rt, "provider_open_failed", err.Error())
	}
	rt.provider = provider
	attachOutboundEmitter(ctx, conn, rt, provider)
	rt.started = true
	rt.startedAt = h.now().UTC()
	return nil
}

// controlUserSpeech handles the two ends of an utterance.
func (h *Handler) controlUserSpeech(
	ctx context.Context,
	conn *websocket.Conn,
	session ConsumedTicket,
	data []byte,
	rt *sessionRuntime,
) (controlOutcome, error) {
	frameType, err := voiceproto.DecodeType(data)
	if err != nil {
		return controlNotMine, nil
	}
	switch frameType {
	case voiceproto.TypeUserSpeechStart, voiceproto.TypeUserSpeechEnd:
	default:
		return controlNotMine, nil
	}
	if !rt.started {
		return controlHandled, rtSendError(ctx, conn, rt, "session_not_started", "send session.start first")
	}
	h.logger.Info("voice user speech frame",
		"session_id", session.SessionID,
		"type", frameType,
		"stage", "asr",
	)
	// The transparent reopen budget belongs to a turn, not to the session. A
	// session-lifetime budget was exhausted by the first upstream hiccup, and
	// since volc-duplex loses its duplex roughly once per turn, the next failure
	// killed the session outright.
	if frameType == voiceproto.TypeUserSpeechStart && rt.reopenAttempted {
		rt.reopenAttempted = false
		h.logger.Info("reopen budget refilled for the new turn",
			"session_id", session.SessionID,
			"stage", "orchestration",
		)
	}
	// B8: the user opening their mouth restarts the ladder. It deliberately does
	// not move the silence window — an utterance that turns out to be incomplete
	// must not buy another three seconds. See SilenceDetector.
	if frameType == voiceproto.TypeUserSpeechStart {
		rt.noteUserSpeechStart()
	}
	if frameType == voiceproto.TypeUserSpeechEnd {
		h.noteUserSpeechEnd(ctx, conn, rt, data)
		return controlHandled, h.startCollectTurn(ctx, conn, rt, session, data)
	}
	return controlHandled, h.forwardToProvider(ctx, conn, session, rt, frameType, data)
}

// controlTurnAbort lets a client abandon a recording without ending it.
//
// I20: iOS already stopped PCM and will not send user.speech.end. Accepting this
// frame (instead of unsupported_frame) keeps the session alive for the next
// user.speech.start.
func (h *Handler) controlTurnAbort(
	ctx context.Context,
	conn *websocket.Conn,
	session ConsumedTicket,
	data []byte,
	rt *sessionRuntime,
) (controlOutcome, error) {
	frameType, err := voiceproto.DecodeType(data)
	if err != nil || frameType != voiceproto.TypeClientTurnAbort {
		return controlNotMine, nil
	}
	var abort voiceproto.ClientTurnAbort
	if err := json.Unmarshal(data, &abort); err != nil {
		return controlHandled, h.invalidFrame(ctx, conn, err.Error())
	}
	if !voiceproto.ValidClientTurnAbortOutcome(abort.Outcome) {
		return controlHandled, h.invalidFrame(ctx, conn,
			"client.turn.abort.outcome must be timeout, user_abandoned, or error")
	}
	h.logger.Info("client.turn.abort accepted",
		"session_id", session.SessionID,
		"turn_id", strings.TrimSpace(abort.TurnID),
		"outcome", abort.Outcome,
		"stage", "asr",
	)
	if !started(rt) {
		return controlHandled, nil
	}
	// B8: an abandoned recording is an unfinished utterance, so the ladder stays
	// armed (docs/78 §5.3 case 2) — but the user has stopped talking, so rescue
	// must stop being suspended.
	rt.noteTurnAbort()
	return controlHandled, h.forwardToProvider(ctx, conn, session, rt, frameType, data)
}

// controlRescue handles the user asking for a rung of the B8 ladder.
//
// It never reaches the provider — the ladder is generated by the gateway's own
// rescue components — so it has no row in providerErrorStrategies.
func (h *Handler) controlRescue(
	ctx context.Context,
	conn *websocket.Conn,
	session ConsumedTicket,
	data []byte,
	rt *sessionRuntime,
) (controlOutcome, error) {
	frameType, err := voiceproto.DecodeType(data)
	if err != nil || frameType != voiceproto.TypeClientRescueRequest {
		return controlNotMine, nil
	}
	var request voiceproto.ClientRescueRequest
	if err := json.Unmarshal(data, &request); err != nil {
		return controlHandled, h.invalidFrame(ctx, conn, err.Error())
	}
	h.requestRescue(ctx, conn, rt, session)
	return controlHandled, nil
}

// controlInterrupt handles the client cutting the assistant off.
func (h *Handler) controlInterrupt(
	ctx context.Context,
	conn *websocket.Conn,
	session ConsumedTicket,
	data []byte,
	rt *sessionRuntime,
) (controlOutcome, error) {
	frameType, err := voiceproto.DecodeType(data)
	if err != nil || frameType != voiceproto.TypeInterrupt {
		return controlNotMine, nil
	}
	h.logger.Info("interrupt received", "session_id", session.SessionID, "stage", "orchestration")
	if !started(rt) {
		return controlHandled, nil
	}
	return controlHandled, h.forwardToProvider(ctx, conn, session, rt, frameType, data)
}

// controlSessionEnd persists the session and closes the connection.
func (h *Handler) controlSessionEnd(
	ctx context.Context,
	conn *websocket.Conn,
	session ConsumedTicket,
	data []byte,
	rt *sessionRuntime,
) (controlOutcome, error) {
	frameType, err := voiceproto.DecodeType(data)
	if err != nil || frameType != voiceproto.TypeSessionEnd {
		return controlNotMine, nil
	}
	var end voiceproto.SessionEnd
	if err := json.Unmarshal(data, &end); err != nil {
		h.logger.Warn("session.end frame decode failed", "err", err, "raw", string(data))
	}
	reason := strings.TrimSpace(end.Reason)
	if reason == "" {
		reason = "user"
	}
	rt.collectWG.Wait()
	durationSec, err := h.persistSession(ctx, rt, session, reason)
	if err != nil {
		h.logger.Warn("session end persist failed", "session_id", session.SessionID, "err", err)
		return controlHandled, rtSendError(ctx, conn, rt, "end_failed", err.Error())
	}
	rt.ended = true
	h.logger.Info("session.end persisted",
		"session_id", session.SessionID,
		"duration_sec", durationSec,
		"utterance_count", len(rt.snapshotUtterances()),
		"unknown_frame_count", rt.unknownFrameCount,
		"turn_rejected", turnRejectionCounts(rt.turn),
		"stage", "orchestration",
	)
	_ = rt.sendJSON(ctx, conn, map[string]any{
		"type":   voiceproto.TypeSessionEnd,
		"reason": "ack",
	})
	_ = conn.Close(websocket.StatusNormalClosure, "session ended")
	return controlHandled, errSessionEnded
}

// controlAuth refuses a second auth. The handshake already consumed one.
func (h *Handler) controlAuth(
	ctx context.Context,
	conn *websocket.Conn,
	_ ConsumedTicket,
	data []byte,
	rt *sessionRuntime,
) (controlOutcome, error) {
	frameType, err := voiceproto.DecodeType(data)
	if err != nil || frameType != voiceproto.TypeAuth {
		return controlNotMine, nil
	}
	return controlHandled, rtSendError(ctx, conn, rt, "already_authenticated", "auth already completed")
}

// unknownFrame ignores a frame type no handler claimed.
//
// Emitting unsupported_frame used to map to iOS .failed and killed live sessions
// (client.turn.abort before 5c2e39f). A later protocol version adding a new
// client frame must not repeat that on an older gateway — so it is counted and
// logged instead.
func (h *Handler) unknownFrame(
	_ context.Context,
	_ *websocket.Conn,
	session ConsumedTicket,
	rt *sessionRuntime,
	frameType string,
) error {
	rt.unknownFrameCount++
	h.logWarn(rt, "unknown_frame",
		"ignoring unknown control frame",
		"session_id", session.SessionID,
		"type", frameType,
		"unknown_frame_count", rt.unknownFrameCount,
	)
	return nil
}
