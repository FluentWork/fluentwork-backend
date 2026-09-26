// Package voicegateway implements the FluentWork voice WSS gateway (B3/B4).
package voicegateway

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"log/slog"
	"net/http"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/FluentWork/fluentwork-backend/internal/conversation"
	"github.com/FluentWork/fluentwork-backend/internal/voiceproto"
	"github.com/FluentWork/fluentwork-backend/pkg/logx"
)

// warnDedupInterval is how often we emit a deduplicated WARN summary.
// Every Nth occurrence within the window gets a full log line with the count.
const warnDedupInterval = 10

// maxClientBinaryFrame bounds one inbound binary frame. coder/websocket reads
// at most 32 KiB per frame by default, which is far below what this protocol
// allows: the client's 20ms framing is a client choice, and its own per-turn cap
// of 60s is ~1.83 MiB of 16 kHz mono PCM16. A larger frame used to fail the read
// and take the session with it, so the ceiling is raised well past that while
// still bounding what a single frame can make the process allocate.
const maxClientBinaryFrame = 4 << 20

// logWarn emits a WARN log with deduplication. Repeated warnings with the same
// key within `window` are collapsed — only the 1st and every `warnDedupInterval`th
// occurrence produce a full log line. This prevents the 80+ identical WARN lines
// seen in docs/20 §1.1 when the provider is stuck in a timeout loop.
func (h *Handler) logWarn(rt *sessionRuntime, key, msg string, args ...any) {
	now := h.now()
	window := 5 * time.Second
	interval := warnDedupInterval

	if rt.warnDedup.key == key && now.Sub(rt.warnDedup.lastAt) < window {
		rt.warnDedup.count++
		rt.warnDedup.lastAt = now
		if rt.warnDedup.count%interval == 0 {
			// Every 10th repeat: emit a summary instead of repeating the full log.
			summaryArgs := append([]any{"dedup_count", rt.warnDedup.count, "first_key", key}, args...)
			h.logger.Warn(msg+" (deduplicated)", summaryArgs...)
		}
		return
	}
	// New key or outside window — emit full line.
	rt.warnDedup.key = key
	rt.warnDedup.lastAt = now
	rt.warnDedup.count = 1
	h.logger.Warn(msg, args...)
}

var errSessionEnded = errors.New("voice session ended")

// ConsumedTicket is the result of a successful one-time ticket consume.
type ConsumedTicket struct {
	TicketID  string `json:"ticket_id"`
	SessionID string `json:"session_id"`
	UserID    string `json:"user_id"`
}

// TicketConsumer validates and consumes a WSS ticket via app-server.
type TicketConsumer interface {
	Consume(ctx context.Context, rawTicket string) (ConsumedTicket, error)
}

// Options configures optional Handler behavior.
type Options struct {
	// InsecureSkipOrigin skips WebSocket Origin checks (local/dev only).
	InsecureSkipOrigin bool
	// IdleTimeout bounds each WebSocket read — a half-open connection detector,
	// not an idle-user reaper. The client's 30s application-level ping keeps
	// resetting it, so it fires only when the client stops sending, which is
	// exactly the case it exists for. See `defaultIdleTimeout` for why "reap a
	// connected but idle user" is a different and unbuilt capability.
	IdleTimeout time.Duration
	// WriteTimeout bounds a single gateway→client write. Expiry closes the
	// connection (see defaultWriteTimeout for why that is the intended
	// outcome, not a side effect). Zero means defaultWriteTimeout.
	WriteTimeout time.Duration
	// RescueTick is how often the B8 silence detector is polled. Zero means
	// defaultRescueTick. It is configurable so tests can run a rescue window
	// measured in milliseconds; production has no reason to leave 500ms.
	RescueTick time.Duration
}

// Handler serves WSS upgrades and the control-frame loop.
type Handler struct {
	consumer     TicketConsumer
	lifecycle    SessionLifecycle
	provider     VoiceProvider
	badgeEmitter *BadgeEmitter
	// B8 stuck rescue. Both must be non-nil for the feature to run; see
	// SetRescueComponents. A nil detector is the feature's off switch.
	silenceDetector    *SilenceDetector
	rescueOrchestrator *RescueOrchestrator
	logger             *slog.Logger
	now                func() time.Time
	insecureSkipOrigin bool
	idleTimeout        time.Duration
	writeTimeout       time.Duration
	rescueTick         time.Duration
	clientASRRequired  bool // B13: gate user.speech.end with empty text when true
}

// NewHandler constructs the voice gateway HTTP/WSS handler.
func NewHandler(
	consumer TicketConsumer,
	lifecycle SessionLifecycle,
	provider VoiceProvider,
	logger *slog.Logger,
	opts Options,
) *Handler {
	if logger == nil {
		logger = slog.Default()
	}
	if provider == nil {
		provider = MockVoiceProvider{}
	}
	idle := opts.IdleTimeout
	if idle <= 0 {
		idle = defaultIdleTimeout
	}
	write := opts.WriteTimeout
	if write <= 0 {
		write = defaultWriteTimeout
	}
	rescueTick := opts.RescueTick
	if rescueTick <= 0 {
		rescueTick = defaultRescueTick
	}
	return &Handler{
		consumer:           consumer,
		lifecycle:          lifecycle,
		provider:           provider,
		logger:             logger.With("component", "voicegateway.handler"),
		now:                time.Now,
		insecureSkipOrigin: opts.InsecureSkipOrigin,
		idleTimeout:        idle,
		writeTimeout:       write,
		rescueTick:         rescueTick,
	}
}

// SetBadgeEmitter wires the optional B12 feedback.badge emitter. Passing nil
// disables hit-detection (the user.speech.end branch becomes a passthrough).
func (h *Handler) SetBadgeEmitter(emitter *BadgeEmitter) {
	h.badgeEmitter = emitter
}

// SetClientASRRequired enables B13 gate: when true, user.speech.end with empty
// text returns an error frame (code: client_asr_required).
func (h *Handler) SetClientASRRequired(required bool) {
	h.clientASRRequired = required
}

// SetRescueComponents wires B8 stuck rescue. Both must be non-nil to enable
// rescue; passing nil for either disables the feature. Call before Mount.
//
// detector is a **template, not the detector sessions use**. A detector holds a
// live silence window, and sessions run concurrently: one shared instance would
// let session A's ai.tts.end open a window that session B's ticker then spends,
// handing A's ladder to B. Handler.loop therefore clones it per session and
// copies only its thresholds, which is the one part that is genuinely global.
// Callers who want to observe detection should watch the ai.rescue.ladder frames
// rather than the detector — that instance never sees a session.
func (h *Handler) SetRescueComponents(detector *SilenceDetector, orchestrator *RescueOrchestrator) {
	h.silenceDetector = detector
	h.rescueOrchestrator = orchestrator
}

// Mount registers health and voice WSS routes on mux.
func (h *Handler) Mount(mux *http.ServeMux) {
	mux.HandleFunc("GET /healthz", h.healthz)
	mux.HandleFunc("GET /readyz", h.healthz)
	mux.HandleFunc("GET /v1/voice", h.serveVoice)
}

func (h *Handler) healthz(w http.ResponseWriter, _ *http.Request) {
	w.Header().Set("Content-Type", "application/json")
	_, _ = io.WriteString(w, `{"status":"ok"}`)
}

func (h *Handler) serveVoice(w http.ResponseWriter, r *http.Request) {
	conn, err := websocket.Accept(w, r, &websocket.AcceptOptions{
		InsecureSkipVerify: h.insecureSkipOrigin,
	})
	if err != nil {
		h.logger.Warn("websocket accept failed", "err", err)
		return
	}
	conn.SetReadLimit(maxClientBinaryFrame)
	defer func() { _ = conn.CloseNow() }()

	ctx := r.Context()
	session, err := h.handshake(ctx, conn)
	if err != nil {
		h.logger.Warn("voice handshake failed", "err", err)
		code := "unauthenticated"
		var ce *ConsumeError
		if errors.As(err, &ce) && ce.Code != "" {
			code = strings.ToLower(ce.Code)
		}
		_ = writeJSON(ctx, conn, voiceproto.ErrorFrame{
			Type:    voiceproto.TypeError,
			Code:    code,
			Message: err.Error(),
		})
		_ = conn.Close(websocket.StatusPolicyViolation, "auth failed")
		return
	}

	h.logger.Info("voice session ready",
		"session_id", session.SessionID,
		"user_id", session.UserID,
	)
	if err := h.loop(ctx, conn, session); err != nil && !errors.Is(err, context.Canceled) {
		h.logger.Info("voice session ended",
			"session_id", session.SessionID,
			"err", err,
		)
	}
}

func (h *Handler) handshake(ctx context.Context, conn *websocket.Conn) (ConsumedTicket, error) {
	seg := logx.Begin(h.logger, "voice.handshake")
	var handshakeErr error
	var endAttrs []any
	defer func() {
		seg.End(handshakeErr, endAttrs...)
	}()

	authCtx, cancel := context.WithTimeout(ctx, 15*time.Second)
	defer cancel()

	typ, data, err := conn.Read(authCtx)
	if err != nil {
		handshakeErr = fmt.Errorf("read auth frame: %w", err)
		return ConsumedTicket{}, handshakeErr
	}
	if typ != websocket.MessageText {
		handshakeErr = errors.New("first frame must be text auth")
		return ConsumedTicket{}, handshakeErr
	}

	frameType, err := voiceproto.DecodeType(data)
	if err != nil {
		handshakeErr = err
		return ConsumedTicket{}, handshakeErr
	}
	if frameType != voiceproto.TypeAuth {
		handshakeErr = fmt.Errorf("expected auth, got %s", frameType)
		return ConsumedTicket{}, handshakeErr
	}

	var auth voiceproto.Auth
	if err := json.Unmarshal(data, &auth); err != nil {
		handshakeErr = fmt.Errorf("decode auth: %w", err)
		return ConsumedTicket{}, handshakeErr
	}
	ticket := strings.TrimSpace(auth.Ticket)
	if ticket == "" {
		handshakeErr = errors.New("ticket is required")
		return ConsumedTicket{}, handshakeErr
	}
	if h.consumer == nil {
		handshakeErr = errors.New("ticket consumer is not configured")
		return ConsumedTicket{}, handshakeErr
	}

	consumed, err := h.consumer.Consume(authCtx, ticket)
	if err != nil {
		handshakeErr = err
		return ConsumedTicket{}, handshakeErr
	}
	if err := writeJSON(authCtx, conn, voiceproto.SessionReady{
		Type:      voiceproto.TypeSessionReady,
		SessionID: consumed.SessionID,
		UserID:    consumed.UserID,
	}); err != nil {
		handshakeErr = err
		return ConsumedTicket{}, handshakeErr
	}
	endAttrs = []any{
		"session_id", consumed.SessionID,
		"user_id", consumed.UserID,
	}
	return consumed, nil
}

type sessionRuntime struct {
	started   bool
	startedAt time.Time
	provider  VoiceProviderSession
	// broken is set the first time the audio forward path fails in a way
	// the session cannot recover from. handleAudio then sends
	// provider_audio_failed and returns a non-nil error so the WSS loop
	// exits immediately (B15 Item 1.2). The flag also drops any binary
	// frame already in flight so we do not re-log the cascade.
	broken bool
	// reopenAttempted records one transparent provider reopen after an
	// upstream audio write failure (backend #43).
	reopenAttempted bool
	// lastStart is the session.start frame this connection opened with, kept
	// so a transparent reopen can open the *same* session rather than a blank
	// one. The reopen used to pass an empty frame, which silently dropped the
	// scene and material the client sent — and would have dropped continuation
	// context the same way. See `77_` P0-9 for the same family of loss on the
	// vendor side.
	lastStart *voiceproto.SessionStart
	// continuation is the resolved tail of the session this one continues, if
	// any. Resolved once, at session.start, and replayed on reopen for the
	// same reason as lastStart.
	continuation []ContinuationTurn
	// B15: warn deduplication state — prevents 80+ identical WARN lines
	// when the audio forward path fails repeatedly (e.g., provider timeout).
	warnDedup struct {
		key      string
		lastAt   time.Time
		count    int
		window   time.Duration
		interval int // log every Nth occurrence (1, 10, 20, ...)
	}
	// rescueLog accumulates this session's B8 stuck points for refine's second
	// input (PRD §5.4.4), reported with the transcript at session end.
	rescueLog rescueLog
	// unknownFrameCount is how many well-formed frames with an unknown
	// type this session ignored. Forward-compat: a future v3 type must
	// not kill a v2 gateway the way client.turn.abort once did.
	unknownFrameCount int
	// ended records that this session was already persisted (by the client's
	// `session.end`). The loop exit path must not persist a second time.
	ended bool
	// writeMu serializes gateway→client writes. collectTurn now runs on its
	// own goroutine so interrupt can be read during WaitTurnResult; its sink
	// emits on that goroutine while the loop still writes ping/interrupt.
	writeMu sync.Mutex
	// writeTimeout bounds each write made under writeMu. A zero value falls
	// back to defaultWriteTimeout so a directly-built sessionRuntime is never
	// left unbounded.
	writeTimeout time.Duration
	// collectWG counts an in-flight user.speech.end / WaitTurnResult.
	collectWG sync.WaitGroup
	// collecting is set while WaitTurnResult is running so a second
	// user.speech.end cannot start a overlapping collect on the same duplex.
	collecting atomic.Bool
	// B8: silence detector for stuck rescue. Per session — see SetRescueComponents
	// for why sharing one across sessions hands ladders to the wrong user.
	silenceDetector *SilenceDetector
	// B8: rescue orchestrator for ladder generation. Unlike the detector this is
	// stateless and safe to share; it is copied onto the runtime only so the
	// emission path does not have to reach back to the Handler.
	rescueOrchestrator *RescueOrchestrator
	// B8: rescueTick is how often the detector is polled for this session.
	rescueTick time.Duration
	// B8: now is the handler's clock, so a session whose rescue window is driven
	// by a frozen or shifted clock stays consistent with the rest of the handler.
	now func() time.Time
	// B8: rescueWG counts the poller plus any ladder generation still in flight.
	// close waits on it, so a session that ends mid-generation does not leave a
	// goroutine writing to a connection nobody owns.
	rescueWG sync.WaitGroup
	// B8: rescueInFlight is set while a ladder is being generated. The poller is
	// the only writer, so this is a "busy" flag rather than a lock; the next
	// rung is simply left unconsumed and picked up on a later tick.
	rescueInFlight atomic.Bool
	// B8: rescueStop closes the poller. Nil means the poller never started.
	rescueStop     chan struct{}
	rescueStopOnce sync.Once
	// B8: rescueMu guards the conversation context handed to the generator. Text
	// deltas arrive on whichever goroutine is relaying provider output — the read
	// loop, the collect goroutine, or the streaming sink — so this is concurrent.
	rescueMu sync.Mutex
	// B8: convCtx accumulates what the generator needs to know about the turn the
	// user is stuck in. LastAIMessage is rebuilt from the turn's text deltas.
	convCtx conversation.ConversationContext
	// B8: rescueTurnID is the turn the ladder belongs to, learned from the AI's
	// own turn-end markers. Empty falls back to the session id at emission time,
	// because the rescue fires before the user's turn_id exists on the wire.
	rescueTurnID string
	// B8: the rung currently being spoken. rescueSpeechTurn is its turn id (empty
	// when nothing is speaking) and rescueSpeechGen is the token that both the
	// ladder goroutine and the read loop use to decide whether a rung is still
	// current. See beginRescueSpeech.
	rescueSpeechMu      sync.Mutex
	rescueSpeechTurn    string
	rescueSpeechGen     uint64
	rescueSpeechTurnRef *uint32
	// sessionID is this runtime's session, for the paths that report about it
	// without having the ticket in hand — the provider's outbound hooks, which
	// run on whichever goroutine is relaying vendor output.
	sessionID string
	// warn reports through the handler's deduplicating warn path. It is carried
	// as a function because those same hooks do not have the *Handler either.
	warn func(key, msg string, args ...any)
	info func(msg string, args ...any)
	// turn is this session's current conversational turn: its identity and the
	// legal ordering of its events. See turn.go for what it replaced.
	turn *Turn
	// audioSeq numbers every binary audio frame this session sends to the client,
	// whoever sends it: the provider streaming the AI's speech, or the rescue
	// ladder. One allocator per client session, because the client's barge-in
	// watermark is per WebSocket session and does not distinguish producers.
	audioSeq *SeqAllocator
	turnRefs *TurnRefAllocator
}

// clock returns the runtime's clock, defaulting to time.Now so a directly-built
// sessionRuntime (tests, future callers) is still bounded by real time.
func (rt *sessionRuntime) clock() time.Time {
	if rt.now == nil {
		return time.Now()
	}
	return rt.now()
}

// rescueEnabled reports whether both halves of B8 were wired.
func (rt *sessionRuntime) rescueEnabled() bool {
	return rt.silenceDetector != nil && rt.rescueOrchestrator != nil
}

// stopRescueLoop closes the poller, if one was started. Idempotent: the loop's
// exit path and close both call it.
func (rt *sessionRuntime) stopRescueLoop() {
	rt.rescueStopOnce.Do(func() {
		if rt.rescueStop != nil {
			close(rt.rescueStop)
		}
	})
}

// resolveWriteTimeout bounds every write made under writeMu. It is a method
// rather than a plain field so that a zero-valued sessionRuntime — which tests
// and any future caller may build — is bounded too, instead of silently
// reproducing the unbounded write this exists to remove.
func (rt *sessionRuntime) resolveWriteTimeout() time.Duration {
	if rt.writeTimeout <= 0 {
		return defaultWriteTimeout
	}
	return rt.writeTimeout
}

// The deadline starts *after* the lock is taken, so it bounds this caller's
// write rather than its wait for the mutex. A second caller can therefore wait
// up to one writeTimeout behind a stalled writer before its own deadline even
// begins; that is bounded, and it is the earlier writer's expiry that ends the
// connection anyway.
func (rt *sessionRuntime) sendJSON(ctx context.Context, conn *websocket.Conn, v any) error {
	rt.writeMu.Lock()
	defer rt.writeMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, rt.resolveWriteTimeout())
	defer cancel()
	return writeJSON(ctx, conn, v)
}

func (rt *sessionRuntime) sendOutbound(ctx context.Context, conn *websocket.Conn, outbound []ProviderOutbound) error {
	// Ask whether anything would reach the wire *before* contending for the
	// lock. The interrupt branch calls this unconditionally while the provider
	// returns `nil, nil`, so without this check the read loop queues behind an
	// in-flight write to send zero bytes — and `interruptedThisTurn` never gets
	// set. (docs/67 §2.1)
	if !writableOutbound(outbound) {
		return nil
	}

	// B8: fold the AI's own output into the rescue state before it goes out.
	// Done here rather than in writeProviderOutbound because that function is
	// also called with the runtime out of reach, and after the write lock is
	// held — and rescue bookkeeping has no business inside a lock that bounds
	// the wire.
	rt.noteProviderOutbound(outbound)
	rt.noteDownlinkLayout(outbound)

	rt.writeMu.Lock()
	defer rt.writeMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, rt.resolveWriteTimeout())
	defer cancel()
	return writeProviderOutbound(ctx, conn, outbound)
}

// sendWithoutRescueState writes outbounds that must not feed the B8 rescue state.
//
// sendOutbound folds provider output into the silence detector — that is how
// "the AI stopped talking" opens a rescue window. The ladder's own audio is
// provider-shaped but is not the AI's turn: feeding it back would open a fresh
// window and reset the rung sequence, so instead of escalating 1 → 2 → 3 the
// ladder would say level 1 forever. Same write lock, no bookkeeping.
func (rt *sessionRuntime) sendWithoutRescueState(ctx context.Context, conn *websocket.Conn, outbound []ProviderOutbound) error {
	if !writableOutbound(outbound) {
		return nil
	}
	rt.noteDownlinkLayout(outbound)
	rt.writeMu.Lock()
	defer rt.writeMu.Unlock()
	ctx, cancel := context.WithTimeout(ctx, rt.resolveWriteTimeout())
	defer cancel()
	return writeProviderOutbound(ctx, conn, outbound)
}

func (rt *sessionRuntime) noteDownlinkLayout(outbound []ProviderOutbound) {
	if rt.info == nil {
		return
	}
	for _, item := range outbound {
		start, ok := item.Control.(voiceproto.AITTSStart)
		if !ok || start.Type != voiceproto.TypeAITTSStart {
			continue
		}
		var turnRef any
		if start.TurnRef != nil {
			turnRef = *start.TurnRef
		}
		rt.info("ai.tts.start selected the downlink audio layout",
			"session_id", rt.sessionID,
			"turn_id", start.TurnID,
			"turn_ref", turnRef,
			"layout", voiceproto.AudioFrameLayoutFor(start.TurnRef).String(),
			"stage", "orchestration",
		)
	}
}

func (h *Handler) loop(ctx context.Context, conn *websocket.Conn, session ConsumedTicket) (loopErr error) {
	rt := &sessionRuntime{
		sessionID:          session.SessionID,
		turn:               NewTurn(session.SessionID),
		audioSeq:           &SeqAllocator{},
		turnRefs:           &TurnRefAllocator{},
		writeTimeout:       h.writeTimeout,
		silenceDetector:    h.rescueDetectorForSession(),
		rescueOrchestrator: h.rescueOrchestrator,
		rescueTick:         h.rescueTick,
		now:                h.now,
	}
	rt.warn = func(key, msg string, args ...any) { h.logWarn(rt, key, msg, args...) }
	rt.info = h.logger.Info
	defer func() {
		rt.close(ctx)
		h.persistOnExit(ctx, rt, session, loopErr)
	}()
	// B8: the poller outlives any single read, so it is started once here rather
	// than per frame. It no-ops when rescue was never wired.
	h.startRescueLoop(ctx, conn, rt, session)
	for {
		readCtx, cancel := context.WithTimeout(ctx, h.idleTimeout)
		typ, data, err := conn.Read(readCtx)
		cancel()
		if err != nil {
			return err
		}
		switch typ {
		case websocket.MessageBinary:
			if err := h.handleAudio(ctx, conn, data, rt, session); err != nil {
				return err
			}
			continue
		case websocket.MessageText:
			if err := h.HandleControl(ctx, conn, session, data, rt); err != nil {
				if errors.Is(err, errSessionEnded) {
					return nil
				}
				return err
			}
		default:
			continue
		}
	}
}

func (h *Handler) handleAudio(
	ctx context.Context,
	conn *websocket.Conn,
	data []byte,
	rt *sessionRuntime,
	session ConsumedTicket,
) error {
	if !rt.started || rt.provider == nil {
		return nil
	}
	// B15: after the first audio-forward failure we return a non-nil error
	// so the WSS loop exits. This guard is belt-and-suspenders if another
	// binary frame is already in flight: drop it without re-logging.
	if rt.broken {
		return nil
	}
	// Log all incoming binary audio frames for debugging
	h.logger.Debug("received binary audio frame",
		"payload_bytes", len(data),
		"session_started", rt.started,
		"provider_nil", rt.provider == nil,
	)
	outbound, err := rt.provider.HandleClientAudio(ctx, data)
	if err != nil {
		// B15-followup (#43): the first upstream failure may be a dead
		// provider connection (Volc idle / network). Reopen once with the
		// same ticket and retry the chunk before giving up on the session.
		if !rt.reopenAttempted {
			rt.reopenAttempted = true
			// Same allocator as the session being replaced, so the numbering
			// continues instead of restarting behind the client's barge-in
			// watermark. There is nothing to carry: see SeqAllocator.
			reopened, openErr := h.provider.Open(ctx, session, rt.audioSeq, rt.turnRefs)
			if openErr == nil {
				// The same frame the session opened with, not a blank one: a
				// reopened session that has forgotten what the practice is
				// about is a different session that happens to share a
				// socket.
				reopenStart := voiceproto.SessionStart{}
				if rt.lastStart != nil {
					reopenStart = *rt.lastStart
				}
				if _, startErr := reopened.Start(ctx, reopenStart, rt.continuation); startErr == nil {
					rt.provider = reopened
					attachOutboundEmitter(ctx, conn, rt, reopened)
					h.logger.Info("provider reopened after audio forward failure; retrying chunk",
						"session_id", session.SessionID,
						"original_err", err,
					)
					retried, retryErr := rt.provider.HandleClientAudio(ctx, data)
					if retryErr == nil {
						return rt.sendOutbound(ctx, conn, retried)
					}
					err = retryErr
				} else {
					err = startErr
				}
			} else {
				err = openErr
			}
		}
		rt.broken = true
		// B15: use logWarn for deduplication — avoids 80+ identical WARN lines
		// when the provider is stuck in a timeout loop.
		h.logWarn(rt, "provider_audio_failed",
			"provider audio forward failed; session will terminate immediately",
			"err", err,
			"reopen_attempted", rt.reopenAttempted,
		)
		// B15 Item 1.2: send the error frame to notify iOS, then return a real
		// error so the loop exits immediately instead of waiting for idle timeout.
		// If the WS is already dead writeJSON will fail — that's fine, the error
		// return below still terminates the session.
		_ = rt.sendJSON(ctx, conn, voiceproto.ErrorFrame{
			Type:    voiceproto.TypeError,
			Code:    "provider_audio_failed",
			Message: err.Error(),
		})
		return fmt.Errorf("provider audio forward failed: %w", err)
	}
	return rt.sendOutbound(ctx, conn, outbound)
}

// startCollectTurn runs WaitTurnResult off the WSS read loop.
//
// The loop used to call HandleClientControl(user.speech.end) synchronously,
// so an interrupt that arrived while the assistant was still generating sat
// in the TCP buffer until collectTurn returned. By then the full reply was
// already persisted and leftover TTS had already been forwarded.
func (h *Handler) startCollectTurn(
	ctx context.Context,
	conn *websocket.Conn,
	rt *sessionRuntime,
	session ConsumedTicket,
	data []byte,
) error {
	if !rt.collecting.CompareAndSwap(false, true) {
		h.logger.Warn("user.speech.end ignored; collect already in flight",
			"session_id", session.SessionID,
		)
		return nil
	}
	rt.collectWG.Add(1)
	go func() {
		defer rt.collectWG.Done()
		defer rt.collecting.Store(false)

		outbound, err := rt.provider.HandleClientControl(ctx, voiceproto.TypeUserSpeechEnd, data)
		if err != nil {
			h.logger.Warn("provider control forward failed",
				"session_id", session.SessionID,
				"type", voiceproto.TypeUserSpeechEnd,
				"err", err,
			)
			if code := providerErrorCode(voiceproto.TypeUserSpeechEnd); code != "" {
				_ = rt.sendJSON(ctx, conn, voiceproto.ErrorFrame{
					Type:    voiceproto.TypeError,
					Code:    code,
					Message: err.Error(),
				})
			}
			return
		}
		if err := rt.sendOutbound(ctx, conn, outbound); err != nil {
			h.logger.Warn("collect turn outbound write failed",
				"session_id", session.SessionID,
				"err", err,
			)
			return
		}
		// One parse for everything that needs the frame: the ASR gate, the badge,
		// and the rescue anchor. It used to be unmarshalled three times here and
		// the "which text is the user's" rule written twice, so a change to one
		// copy could leave the badge and the anchor disagreeing about the same
		// utterance — a mismatch that shows up as a bad anchor weeks later.
		var end voiceproto.UserSpeechEnd
		_ = json.Unmarshal(data, &end)

		if h.clientASRRequired && strings.TrimSpace(end.Text) == "" {
			_ = rt.sendJSON(ctx, conn, voiceproto.ErrorFrame{
				Type:    voiceproto.TypeError,
				Code:    "client_asr_required",
				Message: "user.speech.end.text is required when VOICE_CLIENT_ASR_REQUIRED is enabled",
			})
		}
		// The user's text, resolved once: what they said, or the provider's
		// server-side transcript when the client sent none.
		userText := strings.TrimSpace(end.Text)
		if userText == "" {
			userText = extractServerASRText(outbound)
		}
		// The turn's name, resolved once, by the turn itself. The badge, the
		// ladder and the end-of-session report all say the same word for the same
		// turn because they all read it from here.
		turnID := rt.turn.ID()
		if turnID == "" {
			turnID = session.SessionID
		}
		if h.badgeEmitter != nil {
			h.badgeEmitter.Emit(ctx, realBadgeConn{conn}, session.UserID, session.SessionID, turnID, userText)
		}
		// B8 → D1: the silent path's anchor is whatever the user managed to say
		// after the ladder, so the rescue log needs the same resolved text the
		// badge emitter uses. See rescueLog.noteUserText.
		rt.noteUserUtteranceText(userText)
	}()
	return nil
}

func writeJSON(ctx context.Context, conn *websocket.Conn, v any) error {
	raw, err := json.Marshal(v)
	if err != nil {
		return err
	}
	return conn.Write(ctx, websocket.MessageText, raw)
}

// attachOutboundEmitter wires a provider session's streaming push path to the
// client connection, when the session supports one.
//
// Called wherever rt.provider is (re)assigned. A reopen builds a fresh session,
// which would otherwise stream nowhere — and, because the provider only
// suppresses its end-of-turn text frame when a sink is installed, would keep
// working but lose the streaming behaviour silently.
func attachOutboundEmitter(ctx context.Context, conn *websocket.Conn, rt *sessionRuntime, provider VoiceProviderSession) {
	streamer, ok := provider.(StreamingVoiceProviderSession)
	if !ok {
		return
	}
	streamer.SetOutboundEmitter(func(item ProviderOutbound) error {
		return rt.sendOutbound(ctx, conn, []ProviderOutbound{item})
	})
}

// writableOutbound reports whether any item would put bytes on the wire.
//
// `writeProviderOutbound` writes only `Control` and `Binary`, so this must
// agree with its switch — an item whose only payload is `ServerASRText` is a
// B14 badge carrier and writes nothing.
func writableOutbound(outbound []ProviderOutbound) bool {
	for _, item := range outbound {
		if item.Control != nil || len(item.Binary) > 0 {
			return true
		}
	}
	return false
}

func writeProviderOutbound(ctx context.Context, conn *websocket.Conn, outbound []ProviderOutbound) error {
	// Keep the switch in sync with writableOutbound: it is what decides whether
	// sendOutbound contends for the write lock at all.
	for _, item := range outbound {
		switch {
		case item.Control != nil:
			if err := writeJSON(ctx, conn, item.Control); err != nil {
				return err
			}
		case len(item.Binary) > 0:
			if err := conn.Write(ctx, websocket.MessageBinary, item.Binary); err != nil {
				return err
			}
		}
	}
	return nil
}

// extractServerASRText returns the server-side ASR text from provider outbound.
// B14: The Volcengine provider populates ServerASRText in ProviderOutbound so
// the handler can use it for badge detection when client ASR text is empty.
// resolvedUserText returns the user's utterance for this turn, preferring the
// client's own text and falling back to the provider's server-side ASR — the
// same resolution the badge emitter performs. Empty means the turn produced no
// text we can use as an anchor.

func extractServerASRText(outbound []ProviderOutbound) string {
	for _, item := range outbound {
		if item.ServerASRText != "" {
			return item.ServerASRText
		}
	}
	return ""
}

func (rt *sessionRuntime) snapshotUtterances() []EndUtterance {
	if rt.provider == nil {
		return nil
	}
	return rt.provider.SnapshotUtterances()
}

// snapshotVoiceUsage reports the audio the provider moved, when it can say.
//
// Nil for providers that carry no real conversation (mock, dev-echo): the field
// is optional on the wire for the same reason, and an unreported session must
// leave no cost row rather than a zero-valued one.
func (rt *sessionRuntime) snapshotVoiceUsage() *VoiceUsage {
	reporter, ok := rt.provider.(VoiceUsageReporter)
	if !ok {
		return nil
	}
	usage := reporter.VoiceUsage()
	return &usage
}

// persistSession writes the session and its utterances through app-server and
// returns the duration it recorded. A nil lifecycle is a no-op: local runs
// without app-server still get the rest of the session behaviour.
func (h *Handler) persistSession(
	ctx context.Context,
	rt *sessionRuntime,
	session ConsumedTicket,
	reason string,
) (int, error) {
	durationSec := 0
	if rt.started && !rt.startedAt.IsZero() {
		durationSec = int(h.now().UTC().Sub(rt.startedAt).Seconds())
		if durationSec < 0 {
			durationSec = 0
		}
	}
	if h.lifecycle == nil {
		return durationSec, nil
	}
	endCtx, cancel := context.WithTimeout(ctx, 5*time.Second)
	defer cancel()
	utterances := append([]EndUtterance(nil), rt.snapshotUtterances()...)
	return durationSec, h.lifecycle.End(endCtx, EndSessionRequest{
		SessionID:   session.SessionID,
		DurationSec: durationSec,
		Reason:      reason,
		Utterances:  utterances,
		// P1-1: refine's second input. Sealed here because this is the last
		// moment the runtime can still resolve an open episode's anchor.
		RescueEvents: rt.snapshotRescueEvents(),
		VoiceUsage:   rt.snapshotVoiceUsage(),
	})
}

// persistOnExit writes the session when the WSS loop ends without a client
// `session.end`. Without it a session killed by a provider failure loses every
// utterance — no rows, no session.finished job, no review.
func (h *Handler) persistOnExit(
	ctx context.Context,
	rt *sessionRuntime,
	session ConsumedTicket,
	loopErr error,
) {
	if h.lifecycle == nil || !rt.started || rt.ended {
		return
	}
	reason := "connection_closed"
	switch {
	case rt.broken:
		reason = "provider_audio_failed"
	case errors.Is(loopErr, context.DeadlineExceeded):
		// The read deadline is a **half-open connection detector**, and with a
		// 30s client heartbeat that is the only way it can expire: the peer
		// stopped sending, so it is gone. The name says that.
		//
		// It used to say `idle_timeout`, which promised a category — "the user
		// was idle" — that this mechanism cannot observe. A connected client
		// that keeps pinging never trips it, however long the user says nothing.
		// (`77_` P1-17.) Renaming is cheap because the old value described a
		// state nothing could reach: a reader of the session record should not
		// go looking for idle users in a column that only ever holds corpses.
		reason = "connection_silent"
	}
	// By the time the loop unwinds the connection context is already done.
	// Keep its values (log context) but drop its cancellation, otherwise the
	// app-server call would fail before it left the process.
	_, err := h.persistSession(context.WithoutCancel(ctx), rt, session, reason)
	if err != nil {
		h.logger.Warn("session exit persist failed",
			"session_id", session.SessionID, "reason", reason, "err", err)
		return
	}
	rt.ended = true
	h.logger.Info("session.exit persisted",
		"session_id", session.SessionID,
		"duration_sec", int(h.now().UTC().Sub(rt.startedAt).Seconds()),
		"reason", reason,
		"utterance_count", len(rt.snapshotUtterances()),
		"unknown_frame_count", rt.unknownFrameCount,
		"turn_rejected", turnRejectionCounts(rt.turn),
		"stage", "orchestration",
	)
}

func turnRejectionCounts(t *Turn) map[string]int {
	rejected := t.Rejected()
	out := make(map[string]int, len(rejected))
	for ev, n := range rejected {
		out[ev.String()] = n
	}
	return out
}

func (rt *sessionRuntime) close(ctx context.Context) {
	// B8: stop the poller before waiting on it. Its goroutine selects on
	// rescueStop, so waiting first would deadlock; and a poller left running
	// after the loop returns would keep reading a detector for a session that no
	// longer has a connection.
	rt.stopRescueLoop()
	if rt.provider != nil {
		// Close the upstream first so WaitTurnResult unblocks, then wait for
		// the collect goroutine. Waiting first deadlocks: collect holds the
		// vendor socket open.
		closeCtx, cancel := context.WithTimeout(context.WithoutCancel(ctx), 3*time.Second)
		_ = rt.provider.Close(closeCtx)
		cancel()
	}
	rt.collectWG.Wait()
	// Rescue generation is bounded by its own context.WithTimeout, so this
	// cannot outlast one rung's worth of work.
	rt.rescueWG.Wait()
}
