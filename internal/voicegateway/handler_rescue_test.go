package voicegateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync"
	"testing"
	"time"

	"github.com/coder/websocket"

	"github.com/FluentWork/fluentwork-backend/internal/conversation"
	"github.com/FluentWork/fluentwork-backend/internal/voiceproto"
)

// The handler-level B8 tests run on a **fake clock**.
//
// The detector's three rungs are three seconds apart, and the property under
// test is "a rung fires once its threshold has passed, and not before" — which
// a test that waits on a real 3s clock can only assert by sleeping, and can only
// assert reliably on an idle machine. Driving time from the test instead makes a
// 9-second ladder an assertion rather than a sleep, and lets the negative cases
// be negatives by construction: with the clock frozen, no rung *can* fire, so
// "no ladder arrived" is not a race with a slow tick.
const (
	rescueTestLevel1 = 50 * time.Millisecond
	rescueTestLevel2 = 100 * time.Millisecond
	rescueTestLevel3 = 150 * time.Millisecond
	// rescueTestTick is short so a rung appears within a few milliseconds of the
	// test moving the clock. It is not a threshold: nothing fires unless the
	// *clock* says a threshold has passed.
	rescueTestTick = 5 * time.Millisecond
	// rescueTestWait bounds how long a test waits for a frame it does believe
	// will arrive. Generous on purpose — it only ever expires on a real failure.
	rescueTestWait = 5 * time.Second
)

type rescueTestClock struct {
	mu sync.Mutex
	at time.Time
}

func newRescueTestClock() *rescueTestClock {
	return &rescueTestClock{at: time.Now()}
}

func (c *rescueTestClock) now() time.Time {
	c.mu.Lock()
	defer c.mu.Unlock()
	return c.at
}

func (c *rescueTestClock) advance(d time.Duration) {
	c.mu.Lock()
	defer c.mu.Unlock()
	c.at = c.at.Add(d)
}

func rescueTestThresholds() RescueThresholds {
	return RescueThresholds{
		Level1: rescueTestLevel1,
		Level2: rescueTestLevel2,
		Level3: rescueTestLevel3,
	}
}

// rescueTicketConsumer accepts any non-empty ticket.
type rescueTicketConsumer struct{}

func (rescueTicketConsumer) Consume(_ context.Context, raw string) (ConsumedTicket, error) {
	if strings.TrimSpace(raw) == "" {
		return ConsumedTicket{}, fmt.Errorf("ticket is required")
	}
	return ConsumedTicket{TicketID: "t1", SessionID: "s-rescue", UserID: "u1"}, nil
}

// rescueLifecycle is the do-nothing lifecycle: these tests are about what the
// gateway sends the client, not about what it persists.
type rescueLifecycle struct{}

func (rescueLifecycle) Activate(context.Context, string) error { return nil }
func (rescueLifecycle) End(context.Context, EndSessionRequest) error {
	return nil
}

func (rescueLifecycle) ContinuationContext(context.Context, string, string, int) ([]ContinuationTurn, error) {
	return nil, nil
}

// rescueBootstrap says which AI-turn markers the provider emits when the session
// opens. Production emits both; mock and dev-echo emit only the turn end; and a
// split between them is exactly what the window-opening rule has to survive.
type rescueBootstrap struct {
	ttsEnd  bool
	turnEnd bool
	// turnEndOutcome is the ai.turn.end outcome. Empty behaves like "ok".
	turnEndOutcome string
}

// rescueProviderSession emits the configured bootstrap markers and otherwise
// stays quiet — it never produces an AI turn of its own, so the only window that
// can open is the bootstrap one.
type rescueProviderSession struct {
	bootstrap rescueBootstrap
}

func (s *rescueProviderSession) Start(_ context.Context, _ voiceproto.SessionStart, _ []ContinuationTurn) ([]ProviderOutbound, error) {
	out := []ProviderOutbound{
		{Control: voiceproto.NewAITextDelta("provider-ready", "stub-turn-1", 1)},
	}
	if s.bootstrap.ttsEnd {
		out = append(out, ProviderOutbound{Control: voiceproto.AITTSEnd{
			Type:             voiceproto.TypeAITTSEnd,
			TurnID:           "stub-turn-1",
			CompletionStatus: "ok",
		}})
	}
	if s.bootstrap.turnEnd {
		out = append(out, ProviderOutbound{Control: voiceproto.AITurnEnd{
			Type:    voiceproto.TypeAITurnEnd,
			TurnID:  "stub-turn-1",
			Outcome: s.bootstrap.turnEndOutcome,
		}})
	}
	return out, nil
}

func (s *rescueProviderSession) HandleClientControl(context.Context, string, []byte) ([]ProviderOutbound, error) {
	return nil, nil
}

func (s *rescueProviderSession) HandleClientAudio(context.Context, []byte) ([]ProviderOutbound, error) {
	return nil, nil
}
func (s *rescueProviderSession) SnapshotUtterances() []EndUtterance { return nil }
func (s *rescueProviderSession) Close(context.Context) error        { return nil }

type rescueProvider struct {
	bootstrap rescueBootstrap
}

func (p *rescueProvider) Open(context.Context, ConsumedTicket) (VoiceProviderSession, error) {
	return &rescueProviderSession{bootstrap: p.bootstrap}, nil
}

// rescueTextGenerator answers each rung with a recognisable string, so a test
// can tell which level was asked for even if the frame's level field were wrong.
type rescueTextGenerator struct {
	mu    sync.Mutex
	calls []conversation.RescueLevel
	// convCtx records the last context handed to the generator, which is how the
	// tests prove the AI's own text and the scenario reached it.
	convCtx conversation.ConversationContext
}

func (g *rescueTextGenerator) GenerateRescue(
	_ context.Context,
	level conversation.RescueLevel,
	conv conversation.ConversationContext,
) (string, error) {
	g.mu.Lock()
	defer g.mu.Unlock()
	g.calls = append(g.calls, level)
	g.convCtx = conv
	switch level {
	case conversation.RescueSkeleton:
		return "I think the main risk is...", nil
	case conversation.RescueHint:
		return "先说结论，再说原因", nil
	case conversation.RescueComplete:
		return "I think we should use a blue-green deployment.", nil
	default:
		return "", fmt.Errorf("unexpected rescue level: %d", level)
	}
}

func (g *rescueTextGenerator) levels() []conversation.RescueLevel {
	g.mu.Lock()
	defer g.mu.Unlock()
	return append([]conversation.RescueLevel(nil), g.calls...)
}

func (g *rescueTextGenerator) context() conversation.ConversationContext {
	g.mu.Lock()
	defer g.mu.Unlock()
	return g.convCtx
}

// rescueAudioSynthesizer is a stand-in for the audio path that does not exist yet.
type rescueAudioSynthesizer struct {
	mu    sync.Mutex
	texts []string
}

func (a *rescueAudioSynthesizer) Synthesize(_ context.Context, text, _ string) (string, int64, error) {
	a.mu.Lock()
	defer a.mu.Unlock()
	a.texts = append(a.texts, text)
	return "https://audio.test/rescue.mp3", 2400, nil
}

// rescueRig is one wired gateway plus the knobs the tests drive it with.
type rescueRig struct {
	handler *Handler
	clock   *rescueTestClock
	gen     *rescueTextGenerator
	synth   *rescueAudioSynthesizer
	server  *httptest.Server
}

// newRescueRig builds a gateway with B8 wired. synth may be nil, which is the
// configuration production runs today (no audio path yet).
func newRescueRig(t *testing.T, bootstrap rescueBootstrap, synth *rescueAudioSynthesizer) *rescueRig {
	t.Helper()

	clock := newRescueTestClock()
	gen := &rescueTextGenerator{}

	var synthesizer RescueSynthesizer
	if synth != nil {
		synthesizer = synth
	}

	h := NewHandler(
		rescueTicketConsumer{},
		rescueLifecycle{},
		&rescueProvider{bootstrap: bootstrap},
		slog.New(slog.DiscardHandler),
		Options{InsecureSkipOrigin: true, RescueTick: rescueTestTick},
	)
	h.now = clock.now
	h.SetRescueComponents(
		NewSilenceDetectorWithThresholds(rescueTestThresholds()),
		NewRescueOrchestrator(gen, synthesizer, slog.New(slog.DiscardHandler)),
	)

	mux := http.NewServeMux()
	h.Mount(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return &rescueRig{handler: h, clock: clock, gen: gen, synth: synth, server: srv}
}

// connect completes the handshake and session.start, returning the connection
// and the bootstrap frames the gateway pushed. It returns only once the frames
// that open the rescue window have been *read*, which means the window is
// already open in the gateway too — sendOutbound opens it before writing.
func (r *rescueRig) connect(t *testing.T) (*websocket.Conn, []map[string]any) {
	t.Helper()

	ctx, cancel := context.WithTimeout(context.Background(), rescueTestWait)
	defer cancel()

	conn, _, err := websocket.Dial(ctx, "ws"+strings.TrimPrefix(r.server.URL, "http")+"/v1/voice", nil)
	if err != nil {
		t.Fatalf("dial: %v", err)
	}
	t.Cleanup(func() { _ = conn.CloseNow() })

	if err := conn.Write(ctx, websocket.MessageText, voiceproto.MustMarshal(voiceproto.Auth{
		Type:   voiceproto.TypeAuth,
		Ticket: "good-ticket",
	})); err != nil {
		t.Fatalf("write auth: %v", err)
	}
	if got := rescueReadFrame(ctx, t, conn)["type"]; got != voiceproto.TypeSessionReady {
		t.Fatalf("expected session.ready, got %v", got)
	}

	if err := conn.Write(ctx, websocket.MessageText, voiceproto.MustMarshal(voiceproto.SessionStart{
		Type:       voiceproto.TypeSessionStart,
		SceneType:  "system design discussion",
		MaterialID: "mat-42",
	})); err != nil {
		t.Fatalf("write session.start: %v", err)
	}

	// Read until a window opener goes past, so every caller starts from a
	// deterministic state instead of guessing how many frames the bootstrap is.
	var frames []map[string]any
	for {
		frame := rescueReadFrame(ctx, t, conn)
		frames = append(frames, frame)
		if frame["type"] == voiceproto.TypeAITTSEnd || frame["type"] == voiceproto.TypeAITurnEnd {
			break
		}
	}
	return conn, frames
}

func rescueReadFrame(ctx context.Context, t *testing.T, conn *websocket.Conn) map[string]any {
	t.Helper()
	typ, data, err := conn.Read(ctx)
	if err != nil {
		t.Fatalf("read frame: %v", err)
	}
	if typ != websocket.MessageText {
		t.Fatalf("expected a text frame, got %v", typ)
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		t.Fatalf("decode %s: %v", data, err)
	}
	return out
}

// rescueWaitForType reads until the wanted type arrives or the budget expires.
// A timeout is a failure: callers only ask for frames they expect.
func rescueWaitForType(ctx context.Context, t *testing.T, conn *websocket.Conn, want string) map[string]any {
	t.Helper()
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("waitForType(%s): %v", want, err)
		}
		var raw map[string]any
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Fatalf("decode frame: %v", err)
		}
		if raw["type"] == want {
			return raw
		}
	}
}

// rescueExpectNoType consumes frames until the connection goes quiet and fails
// if the unwanted type ever appears.
func rescueExpectNoType(ctx context.Context, t *testing.T, conn *websocket.Conn, unwanted string) {
	t.Helper()
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			return // quiet for the whole window: nothing unwanted was sent
		}
		var raw map[string]any
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Fatalf("decode frame: %v", err)
		}
		if raw["type"] == unwanted {
			t.Fatalf("gateway sent %s when it should not have: %#v", unwanted, raw)
		}
	}
}

func rescueSendFrame(ctx context.Context, t *testing.T, conn *websocket.Conn, v any) {
	t.Helper()
	if err := conn.Write(ctx, websocket.MessageText, voiceproto.MustMarshal(v)); err != nil {
		t.Fatalf("write frame: %v", err)
	}
}

// rescueLevelOf reads a map-decoded ladder's level as an int, failing on any
// shape other than the integer the contract promises.
func rescueLevelOf(t *testing.T, frame map[string]any) int {
	t.Helper()
	level, ok := frame["level"].(float64)
	if !ok {
		t.Fatalf("ladder has no numeric level: %#v", frame)
	}
	return int(level)
}

// The happy path: the AI stops talking, the user says nothing, and the three
// rungs arrive one threshold apart.
func TestHandler_Rescue_SilentUserReceivesWholeLadder(t *testing.T) {
	t.Parallel()

	rig := newRescueRig(t, rescueBootstrap{ttsEnd: true, turnEnd: true}, &rescueAudioSynthesizer{})
	conn, _ := rig.connect(t)

	readCtx, cancel := context.WithTimeout(context.Background(), rescueTestWait)
	defer cancel()

	want := []struct {
		advance time.Duration
		level   int
		text    string
	}{
		{rescueTestLevel1, 1, "I think the main risk is..."},
		{rescueTestLevel2 - rescueTestLevel1, 2, "先说结论，再说原因"},
		{rescueTestLevel3 - rescueTestLevel2, 3, "I think we should use a blue-green deployment."},
	}

	for _, step := range want {
		rig.clock.advance(step.advance)
		frame := rescueWaitForType(readCtx, t, conn, voiceproto.TypeRescueLadder)

		if got := rescueLevelOf(t, frame); got != step.level {
			t.Fatalf("level = %d, want %d (%#v)", got, step.level, frame)
		}
		if got := frame["text"]; got != step.text {
			t.Errorf("level %d text = %v, want %q", step.level, got, step.text)
		}
		// The ladder belongs to the AI's turn: user.speech.start carries no
		// turn_id and the user has not spoken, so there is no other id to use.
		if got := frame["turn_id"]; got != "stub-turn-1" {
			t.Errorf("level %d turn_id = %v, want stub-turn-1", step.level, got)
		}
		if got := frame["audio_url"]; got != "https://audio.test/rescue.mp3" {
			t.Errorf("level %d audio_url = %v, want the synthesized URL", step.level, got)
		}
		if got, ok := frame["ts"].(float64); !ok || got <= 0 {
			t.Errorf("level %d ts = %v, want a positive millisecond timestamp", step.level, frame["ts"])
		}
	}

	// Three rungs, then silence: a ladder that keeps firing stops being help and
	// starts being noise (docs/78 §5.3 case 3).
	rig.clock.advance(10 * time.Second)
	quietCtx, cancelQuiet := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancelQuiet()
	rescueExpectNoType(quietCtx, t, conn, voiceproto.TypeRescueLadder)

	if got := len(rig.gen.levels()); got != 3 {
		t.Errorf("generator called %d times, want 3", got)
	}
	if got := len(rig.synth.texts); got != 3 {
		t.Errorf("synthesizer called %d times, want 3", got)
	}
}

// The AI's own words and the session's scene must reach the generator: a ladder
// generated against no context is generic encouragement, which is the failure
// mode the level-3 example exists to avoid.
func TestHandler_Rescue_GeneratorSeesTurnContext(t *testing.T) {
	t.Parallel()

	rig := newRescueRig(t, rescueBootstrap{ttsEnd: true, turnEnd: true}, nil)
	conn, _ := rig.connect(t)

	readCtx, cancel := context.WithTimeout(context.Background(), rescueTestWait)
	defer cancel()

	rig.clock.advance(rescueTestLevel1)
	rescueWaitForType(readCtx, t, conn, voiceproto.TypeRescueLadder)

	got := rig.gen.context()
	if got.ScenarioContext != "system design discussion" {
		t.Errorf("scenario = %q, want the session's scene_type", got.ScenarioContext)
	}
	if got.LastAIMessage != "provider-ready" {
		t.Errorf("last AI message = %q, want the AI's own deltas", got.LastAIMessage)
	}
}

// A gateway with no audio path still delivers the prompt, and says so by leaving
// audio_url empty rather than inventing a URL iOS would fail to fetch.
func TestHandler_Rescue_WithoutSynthesizerEmitsTextOnlyLadder(t *testing.T) {
	t.Parallel()

	rig := newRescueRig(t, rescueBootstrap{ttsEnd: true, turnEnd: true}, nil)
	conn, _ := rig.connect(t)

	readCtx, cancel := context.WithTimeout(context.Background(), rescueTestWait)
	defer cancel()

	rig.clock.advance(rescueTestLevel1)
	frame := rescueWaitForType(readCtx, t, conn, voiceproto.TypeRescueLadder)

	if frame["text"] == "" {
		t.Error("text-only ladder carried no text")
	}
	if got := frame["audio_url"]; got != "" {
		t.Errorf("audio_url = %v, want empty without a synthesizer", got)
	}
}

// ai.tts.end alone opens the window. Production emits both markers, but a
// gateway that waited for ai.turn.end would leave any provider that ends its
// turn differently unrescued.
func TestHandler_Rescue_AITTSEndAloneOpensTheWindow(t *testing.T) {
	t.Parallel()

	rig := newRescueRig(t, rescueBootstrap{ttsEnd: true}, nil)
	conn, _ := rig.connect(t)

	readCtx, cancel := context.WithTimeout(context.Background(), rescueTestWait)
	defer cancel()

	rig.clock.advance(rescueTestLevel1)
	frame := rescueWaitForType(readCtx, t, conn, voiceproto.TypeRescueLadder)
	if got := rescueLevelOf(t, frame); got != 1 {
		t.Fatalf("level = %d, want 1", got)
	}
}

// ai.turn.end alone opens the window too: mock and dev-echo never emit a TTS end
// marker, and a session on those providers must still be rescuable.
func TestHandler_Rescue_AITurnEndAloneOpensTheWindow(t *testing.T) {
	t.Parallel()

	rig := newRescueRig(t, rescueBootstrap{turnEnd: true}, nil)
	conn, _ := rig.connect(t)

	readCtx, cancel := context.WithTimeout(context.Background(), rescueTestWait)
	defer cancel()

	rig.clock.advance(rescueTestLevel1)
	frame := rescueWaitForType(readCtx, t, conn, voiceproto.TypeRescueLadder)
	if got := rescueLevelOf(t, frame); got != 1 {
		t.Fatalf("level = %d, want 1", got)
	}
}

// A user who answers in full is not stuck, and must not be talked over. The
// clock is advanced far past every threshold afterwards, so the only thing that
// can keep the ladder away is the complete utterance having closed the window.
func TestHandler_Rescue_CompleteAnswerIsNotRescued(t *testing.T) {
	t.Parallel()

	rig := newRescueRig(t, rescueBootstrap{ttsEnd: true, turnEnd: true}, nil)
	conn, _ := rig.connect(t)

	ctx, cancel := context.WithTimeout(context.Background(), rescueTestWait)
	defer cancel()

	rescueSendFrame(ctx, t, conn, voiceproto.UserSpeechStart{Type: voiceproto.TypeUserSpeechStart})
	rescueSendFrame(ctx, t, conn, voiceproto.UserSpeechEnd{
		Type:   voiceproto.TypeUserSpeechEnd,
		Text:   "I agree with your point, the trade-off is worth it",
		TurnID: "t-user-1",
	})

	rig.clock.advance(30 * time.Second)

	quietCtx, cancelQuiet := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancelQuiet()
	rescueExpectNoType(quietCtx, t, conn, voiceproto.TypeRescueLadder)

	if got := len(rig.gen.levels()); got != 0 {
		t.Errorf("generator ran %d times for a user who answered, want 0", got)
	}
}

// While the user is speaking, a skeleton prompt would talk over them.
func TestHandler_Rescue_DoesNotFireWhileTheUserIsSpeaking(t *testing.T) {
	t.Parallel()

	rig := newRescueRig(t, rescueBootstrap{ttsEnd: true, turnEnd: true}, nil)
	conn, _ := rig.connect(t)

	ctx, cancel := context.WithTimeout(context.Background(), rescueTestWait)
	defer cancel()

	rescueSendFrame(ctx, t, conn, voiceproto.UserSpeechStart{Type: voiceproto.TypeUserSpeechStart})
	rig.clock.advance(30 * time.Second)

	quietCtx, cancelQuiet := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancelQuiet()
	rescueExpectNoType(quietCtx, t, conn, voiceproto.TypeRescueLadder)
}

// docs/78 §5.3 case 1, end to end: "I think" is not an answer. The user's own
// clock keeps running, so the skeleton is due at the original threshold rather
// than three seconds after they gave up mid-sentence.
func TestHandler_Rescue_IncompleteAnswerKeepsTheClockRunning(t *testing.T) {
	t.Parallel()

	rig := newRescueRig(t, rescueBootstrap{ttsEnd: true, turnEnd: true}, nil)
	conn, _ := rig.connect(t)

	ctx, cancel := context.WithTimeout(context.Background(), rescueTestWait)
	defer cancel()

	rescueSendFrame(ctx, t, conn, voiceproto.UserSpeechStart{Type: voiceproto.TypeUserSpeechStart})
	// Still short of the first rung when the user trails off.
	rig.clock.advance(rescueTestLevel1 / 5)
	rescueSendFrame(ctx, t, conn, voiceproto.UserSpeechEnd{
		Type: voiceproto.TypeUserSpeechEnd,
		Text: "I think",
	})

	// The window did not move, so the remaining 4/5 of the threshold is enough.
	rig.clock.advance(rescueTestLevel1 - rescueTestLevel1/5)
	frame := rescueWaitForType(ctx, t, conn, voiceproto.TypeRescueLadder)

	if got := rescueLevelOf(t, frame); got != 1 {
		t.Fatalf("level = %d, want 1 — an incomplete utterance must not restart the clock", got)
	}
}

// The feature's off switch. An unwired gateway must behave exactly as it did
// before B8 existed: with the clock frozen no rung can fire, and this test is the
// proof that nothing else does either.
func TestHandler_Rescue_NotWiredNeverFires(t *testing.T) {
	t.Parallel()

	clock := newRescueTestClock()
	h := NewHandler(
		rescueTicketConsumer{},
		rescueLifecycle{},
		&rescueProvider{bootstrap: rescueBootstrap{ttsEnd: true, turnEnd: true}},
		slog.New(slog.DiscardHandler),
		Options{InsecureSkipOrigin: true, RescueTick: rescueTestTick},
	)
	h.now = clock.now
	// Deliberately no SetRescueComponents.

	mux := http.NewServeMux()
	h.Mount(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	rig := &rescueRig{handler: h, clock: clock, gen: &rescueTextGenerator{}, server: srv}
	conn, _ := rig.connect(t)

	rig.clock.advance(10 * time.Minute)

	quietCtx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	rescueExpectNoType(quietCtx, t, conn, voiceproto.TypeRescueLadder)
}

// A detector holds a live window, so two sessions must never share one: session
// A's ai.tts.end would otherwise open a window that B's ticker spends, handing
// A's ladder to B. The template's own state must stay untouched for the same
// reason — it is configuration, not a detector.
func TestHandler_RescueDetectorIsPerSessionAndLeavesTheTemplateAlone(t *testing.T) {
	t.Parallel()

	rig := newRescueRig(t, rescueBootstrap{ttsEnd: true, turnEnd: true}, nil)

	first := rig.handler.rescueDetectorForSession()
	second := rig.handler.rescueDetectorForSession()

	if first == nil || second == nil {
		t.Fatal("rescueDetectorForSession returned nil while rescue was wired")
	}
	if first == second {
		t.Fatal("two sessions were handed the same detector — their windows are shared")
	}
	if first == rig.handler.silenceDetector || second == rig.handler.silenceDetector {
		t.Fatal("a session was handed the wired template detector")
	}
	if got, want := first.Thresholds(), rescueTestThresholds(); got != want {
		t.Errorf("cloned thresholds = %+v, want the template's %+v", got, want)
	}

	// The template must not accumulate state from either clone.
	if _, level, elapsed := rig.handler.silenceDetector.GetState(time.Now()); level != 0 || elapsed != 0 {
		t.Errorf("template detector was mutated: level=%d elapsed=%v", level, elapsed)
	}

	// And an unwired gateway must hand out nothing at all.
	bare := NewHandler(rescueTicketConsumer{}, rescueLifecycle{}, nil, slog.New(slog.DiscardHandler), Options{})
	if got := bare.rescueDetectorForSession(); got != nil {
		t.Errorf("unwired handler produced a detector: %+v", got)
	}
}

// An abandoned recording is not an answer either, but it does mean the user has
// stopped talking — so rescue resumes rather than staying suspended forever.
func TestHandler_Rescue_TurnAbortResumesRescue(t *testing.T) {
	t.Parallel()

	rig := newRescueRig(t, rescueBootstrap{ttsEnd: true, turnEnd: true}, nil)
	conn, _ := rig.connect(t)

	ctx, cancel := context.WithTimeout(context.Background(), rescueTestWait)
	defer cancel()

	rescueSendFrame(ctx, t, conn, voiceproto.UserSpeechStart{Type: voiceproto.TypeUserSpeechStart})
	rescueSendFrame(ctx, t, conn, voiceproto.ClientTurnAbort{
		Type:    voiceproto.TypeClientTurnAbort,
		TurnID:  "t-abort-1",
		Outcome: voiceproto.ClientTurnAbortUserAbandoned,
	})

	rig.clock.advance(rescueTestLevel1)
	frame := rescueWaitForType(ctx, t, conn, voiceproto.TypeRescueLadder)

	if got := rescueLevelOf(t, frame); got != 1 {
		t.Fatalf("level = %d, want 1 after an abandoned recording", got)
	}
}

// A turn that failed asked the user nothing, so there is nothing to be stuck on.
// B15's 30s turn timeout ends a turn this way and arrives after the ladder has
// already spent its rungs, so without this guard an unanswered turn would be
// re-nagged from 33s by a feature whose purpose is to stop nagging.
func TestHandler_Rescue_FailedTurnDoesNotOpenWindow(t *testing.T) {
	t.Parallel()

	for _, outcome := range []string{voiceproto.TurnOutcomeTimeout, voiceproto.TurnOutcomeError} {
		t.Run(outcome, func(t *testing.T) {
			t.Parallel()

			rig := newRescueRig(t, rescueBootstrap{turnEnd: true, turnEndOutcome: outcome}, nil)
			conn, _ := rig.connect(t)

			rig.clock.advance(30 * time.Second)

			quietCtx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
			defer cancel()
			rescueExpectNoType(quietCtx, t, conn, voiceproto.TypeRescueLadder)

			if got := len(rig.gen.levels()); got != 0 {
				t.Errorf("generator ran %d times for a failed turn, want 0", got)
			}
		})
	}
}

// "partial" is not a failure: the AI did reply, the reply was just cut short, so
// the user still has something to answer and the ladder still applies.
func TestHandler_Rescue_PartialTurnStillOpensWindow(t *testing.T) {
	t.Parallel()

	rig := newRescueRig(t, rescueBootstrap{turnEnd: true, turnEndOutcome: voiceproto.TurnOutcomePartial}, nil)
	conn, _ := rig.connect(t)

	readCtx, cancel := context.WithTimeout(context.Background(), rescueTestWait)
	defer cancel()

	rig.clock.advance(rescueTestLevel1)
	frame := rescueWaitForType(readCtx, t, conn, voiceproto.TypeRescueLadder)
	if got := rescueLevelOf(t, frame); got != 1 {
		t.Fatalf("level = %d, want 1", got)
	}
}
