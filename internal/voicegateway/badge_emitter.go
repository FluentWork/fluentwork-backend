package voicegateway

import (
	"container/list"
	"context"
	"encoding/json"
	"errors"
	"log/slog"
	"strings"
	"sync"
	"sync/atomic"
	"time"

	"github.com/coder/websocket"

	"github.com/FluentWork/fluentwork-backend/internal/session"
	"github.com/FluentWork/fluentwork-backend/internal/voiceproto"
)

// DefaultHitDetectTimeout is the hard upper bound the gateway applies to one
// hit-detection call (B12 budget). Anything beyond this is treated as a miss
// and logged at warn level — the user.speech.end frame must not block on it.
const DefaultHitDetectTimeout = 800 * time.Millisecond

// DefaultDedupeTTL is how long a (session, turn, phrase_block) tuple stays in
// the dedupe LRU. One conversational turn is the lifetime we want to suppress
// across (catches re-emit from vendor backpressure + handler retries); the
// 5s ceiling protects against unbounded growth from pathological clients.
const DefaultDedupeTTL = 5 * time.Second

// DefaultDedupeCapacity bounds the dedupe cache. Each session can produce
// many keys per turn if the detector fires repeatedly; 1024 covers ~200 turns
// of 5 keys/turn before the LRU starts evicting, which is well above any
// realistic single-session volume.
const DefaultDedupeCapacity = 1024

// BadgeEmitter runs hit detection asynchronously after a user.speech.end frame
// and writes a feedback.badge frame to the client on hit.
//
// The emitter is intentionally side-effect-free with respect to the caller's
// control frame loop: Emit returns immediately and the detection/write work
// happens on a dedicated goroutine so the user.speech.end ack can proceed.
type BadgeEmitter struct {
	detector *session.HitDetector
	logger   *slog.Logger
	timeout  time.Duration
	dedupe   *dedupeLRU
	now      func() time.Time
	wg       *sync.WaitGroup

	// counters are atomic so Stats() can read them without blocking the
	// emit goroutines. They are best-effort observability, not a billing
	// source of truth — slight skew under burst load is acceptable.
	statsEmitCalls    atomic.Int64 // total Emit/EmitSync calls past the empty/dedup skip
	statsEmptySkips   atomic.Int64 // Emit dropped because text was empty
	statsMisses       atomic.Int64 // detector returned a non-hit decision
	statsHits         atomic.Int64 // badge frame written to client
	statsDedupDropped atomic.Int64 // LRU suppressed a re-emit
	statsDetectErrors atomic.Int64 // detector returned an error (incl. timeout)
	statsWriteErrors  atomic.Int64 // conn.WriteBadge failed
}

// BadgeEmitterOptions configures the emitter. Zero values fall back to the
// package defaults.
type BadgeEmitterOptions struct {
	// Timeout caps one Detect call. Zero uses DefaultHitDetectTimeout.
	Timeout time.Duration
	// DedupeTTL bounds a dedupe key's lifetime. Zero uses DefaultDedupeTTL.
	DedupeTTL time.Duration
	// DedupeCapacity bounds the LRU size. Zero uses DefaultDedupeCapacity.
	DedupeCapacity int
	// Now is the clock source (tests inject a fake clock).
	Now func() time.Time
}

// NewBadgeEmitterForTest is a test-only convenience constructor that mirrors
// NewBadgeEmitter without the variadic WaitGroup parameter. It returns nil
// when detector is nil so callers can early-skip with t.Skip(...) in
// environments that cannot construct a HitDetector (e.g. testLocal hermetic
// suites without the corpus BlockSourceAdapter wired in).
func NewBadgeEmitterForTest(detector *session.HitDetector, logger *slog.Logger, opts BadgeEmitterOptions) *BadgeEmitter {
	if detector == nil {
		return nil
	}
	return NewBadgeEmitter(detector, logger, opts)
}

// NewBadgeEmitter wires the emitter to the detector that already wraps the
// session.BlockSource (typically the corpus.BlockSourceAdapter). If wg is
// provided (e.g. from a test), each Emit goroutine calls wg.Done so callers
// can block until the badge work completes before asserting on connection state.
func NewBadgeEmitter(detector *session.HitDetector, logger *slog.Logger, opts BadgeEmitterOptions, wg ...*sync.WaitGroup) *BadgeEmitter {
	if logger == nil {
		logger = slog.Default()
	}
	if opts.Timeout <= 0 {
		opts.Timeout = DefaultHitDetectTimeout
	}
	if opts.DedupeTTL <= 0 {
		opts.DedupeTTL = DefaultDedupeTTL
	}
	if opts.DedupeCapacity <= 0 {
		opts.DedupeCapacity = DefaultDedupeCapacity
	}
	now := opts.Now
	if now == nil {
		now = time.Now
	}
	var wgPtr *sync.WaitGroup
	if len(wg) > 0 {
		wgPtr = wg[0]
	}
	return &BadgeEmitter{
		detector: detector,
		logger:   logger.With("component", "voicegateway.badge_emitter"),
		timeout:  opts.Timeout,
		dedupe:   newDedupeLRU(opts.DedupeCapacity, opts.DedupeTTL, now),
		now:      now,
		wg:       wgPtr,
	}
}

// badgeConn abstracts the write surface used by doEmit. It is satisfied by
// *websocket.Conn in production and by test fakes that only need Write.
type badgeConn interface {
	WriteBadge(ctx context.Context, raw []byte) error
}

type realBadgeConn struct{ conn *websocket.Conn }

func (r realBadgeConn) WriteBadge(ctx context.Context, raw []byte) error {
	return r.conn.Write(ctx, websocket.MessageText, raw)
}

// Emit is the public entry point. It is safe to call from the WSS handler's
// control loop on every user.speech.end — work happens on a goroutine and
// the caller is not blocked. conn must satisfy badgeConn; the handler passes
// a realBadgeConn wrapping *websocket.Conn while tests pass *fakeWSConn.
func (e *BadgeEmitter) Emit(ctx context.Context, conn badgeConn, userID, sessionID, turnID, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		// No client ASR payload → skip silently. This is the common path for
		// server-side ASR clients.
		e.statsEmptySkips.Add(1)
		return
	}
	if e.detector == nil || conn == nil {
		return
	}
	if e.wg != nil {
		e.wg.Add(1)
	}
	e.statsEmitCalls.Add(1)
	go func() {
		e.doEmit(ctx, conn, userID, sessionID, turnID, text, func() {
			if e.wg != nil {
				e.wg.Done()
			}
		})
	}()
}

// EmitSync runs the detection synchronously and waits for completion. Use in
// tests and any caller that needs a deterministic ordering guarantee.
func (e *BadgeEmitter) EmitSync(ctx context.Context, conn badgeConn, userID, sessionID, turnID, text string) {
	text = strings.TrimSpace(text)
	if text == "" {
		e.statsEmptySkips.Add(1)
		return
	}
	if e.detector == nil || conn == nil {
		return
	}
	e.statsEmitCalls.Add(1)
	e.doEmit(ctx, conn, userID, sessionID, turnID, text, func() {})
}

// doEmit performs the hit-detection + badge-write work. It is called by both
// Emit (via a goroutine) and EmitSync (directly in the caller's goroutine).
// onDone is called exactly once before doEmit returns, regardless of outcome.
// Emit passes a wg.Done closure; EmitSync passes a no-op.
func (e *BadgeEmitter) doEmit(parent context.Context, conn badgeConn, userID, sessionID, turnID, text string, onDone func()) {
	ctx, cancel := context.WithTimeout(parent, e.timeout)
	defer cancel()

	decision, err := e.detector.Detect(ctx, session.HitDetectRequest{
		UserID:    userID,
		SessionID: sessionID,
		TurnID:    turnID,
		Text:      text,
	})
	if err != nil {
		e.statsDetectErrors.Add(1)
		// Bad request (empty user_id, oversize text) — already counted as a miss.
		if !errors.Is(err, session.ErrHitDetectInvalidRequest) {
			e.logger.Warn("hit detect failed",
				"session_id", sessionID,
				"turn_id", turnID,
				"err", err,
			)
		}
		onDone()
		return
	}
	if decision.Kind != session.HitDecisionHit {
		e.statsMisses.Add(1)
		// Misses are not logged at info; they are the dominant path.
		onDone()
		return
	}

	badge := voiceproto.NewFeedbackBadge(
		decision.Hit.BadgeLabel,
		decision.Hit.ID,
		decision.Hit.Tier,
		sessionID,
		turnID,
	)
	if badge.DedupeKey == "" {
		// ComposeBadgeDedupeKey returned "" because something was empty; the
		// detector guarantees block id is set, so this can only happen if the
		// caller passed an empty session/turn. Skip emission.
		onDone()
		return
	}

	if e.dedupe.Allow(badge.DedupeKey) {
		// Duplicate within the dedupe TTL — drop silently.
		e.statsDedupDropped.Add(1)
		onDone()
		return
	}

	raw, err := json.Marshal(badge)
	if err != nil {
		onDone()
		e.logger.Error("marshal badge", "err", err)
		return
	}
	writeCtx, cancelWrite := context.WithTimeout(parent, e.timeout)
	defer cancelWrite()
	if err := conn.WriteBadge(writeCtx, raw); err != nil {
		e.statsWriteErrors.Add(1)
		e.logger.Warn("badge write failed",
			"session_id", sessionID,
			"turn_id", turnID,
			"phrase_block_id", decision.Hit.ID,
			"score", decision.Hit.Score,
			"err", err,
		)
		onDone()
		return
	}
	e.statsHits.Add(1)
	e.logger.Info("feedback.badge emitted",
		"session_id", sessionID,
		"turn_id", turnID,
		"phrase_block_id", decision.Hit.ID,
		"score", decision.Hit.Score,
		"detect_duration", decision.Duration,
		"tier", decision.Hit.Tier,
	)
	onDone()
}

// dedupeLRU is a TTL + capacity LRU keyed by feedback.badge dedupe keys.
// Both reads and writes touch the mutex — the read path is the hot one
// (one Allow per Detect attempt) but the work per call is O(1).
type dedupeLRU struct {
	mu       sync.Mutex
	capacity int
	ttl      time.Duration
	now      func() time.Time
	keys     map[string]*list.Element
	order    *list.List
}

type dedupeEntry struct {
	key       string
	expiresAt time.Time
}

func newDedupeLRU(capacity int, ttl time.Duration, now func() time.Time) *dedupeLRU {
	if capacity <= 0 {
		capacity = DefaultDedupeCapacity
	}
	if ttl <= 0 {
		ttl = DefaultDedupeTTL
	}
	return &dedupeLRU{
		capacity: capacity,
		ttl:      ttl,
		now:      now,
		keys:     make(map[string]*list.Element, capacity),
		order:    list.New(),
	}
}

// Allow reports whether the key has been seen within the TTL window. It is
// false when the key is fresh (i.e. callers should emit) and true when the
// key was already emitted recently (callers should suppress). A successful
// emission also records the key.
func (d *dedupeLRU) Allow(key string) bool {
	if key == "" {
		return true
	}
	now := d.now()
	d.mu.Lock()
	defer d.mu.Unlock()

	if elem, ok := d.keys[key]; ok {
		entry := elem.Value.(*dedupeEntry)
		if now.Before(entry.expiresAt) {
			d.order.MoveToFront(elem)
			return true
		}
		// Expired — evict and treat as fresh.
		d.order.Remove(elem)
		delete(d.keys, key)
	}

	entry := &dedupeEntry{key: key, expiresAt: now.Add(d.ttl)}
	elem := d.order.PushFront(entry)
	d.keys[key] = elem
	for d.order.Len() > d.capacity {
		oldest := d.order.Back()
		if oldest == nil {
			break
		}
		d.order.Remove(oldest)
		delete(d.keys, oldest.Value.(*dedupeEntry).key)
	}
	return false
}

// Len reports the current cache size (tests only).
func (d *dedupeLRU) Len() int {
	d.mu.Lock()
	defer d.mu.Unlock()
	return d.order.Len()
}

// BadgeEmitterStats is a point-in-time snapshot of BadgeEmitter counters.
// Counters are best-effort: they can drift slightly under burst load because
// reads are atomic but the underlying events are not transactional.
type BadgeEmitterStats struct {
	EmitCalls    int64 // total Emit/EmitSync calls past the empty/dedup skip
	EmptySkips   int64 // Emit dropped because text was empty after TrimSpace
	Misses       int64 // detector returned a non-hit decision
	Hits         int64 // badge frame successfully written to client
	DedupDropped int64 // LRU suppressed a re-emit
	DetectErrors int64 // detector returned an error (incl. timeout)
	WriteErrors  int64 // conn.WriteBadge failed
}

// Stats returns a snapshot of the emitter's counters. Safe to call from any
// goroutine; counts reflect activity since emitter construction.
func (e *BadgeEmitter) Stats() BadgeEmitterStats {
	return BadgeEmitterStats{
		EmitCalls:    e.statsEmitCalls.Load(),
		EmptySkips:   e.statsEmptySkips.Load(),
		Misses:       e.statsMisses.Load(),
		Hits:         e.statsHits.Load(),
		DedupDropped: e.statsDedupDropped.Load(),
		DetectErrors: e.statsDetectErrors.Load(),
		WriteErrors:  e.statsWriteErrors.Load(),
	}
}
