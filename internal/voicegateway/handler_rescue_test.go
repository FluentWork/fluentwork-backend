package voicegateway

import (
	"context"
	"encoding/json"
	"fmt"
	"log/slog"
	"net/http"
	"net/http/httptest"
	"slices"
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

// rescueAITurn describes the markers one AI turn emits as it ends.
//
// It describes a *turn* — one the AI took after the user spoke. The frame that
// announces the session opening is deliberately not modelled by it: that frame
// belongs to no turn, and the whole point of
// TestHandler_Rescue_SessionOpenAnnouncementDoesNotOpenTheWindow is that it
// opens nothing.
type rescueAITurn struct {
	ttsEnd  bool
	turnEnd bool
	// turnEndOutcome is the ai.turn.end outcome. Empty behaves like "ok".
	turnEndOutcome string
}

// rescueProviderSession announces the session opening the way every real
// provider does, then replies to the user's *first* utterance with one AI turn
// and stays silent after that.
//
// One reply rather than a reply per utterance, because several tests need to
// watch what a user's own utterance does to a window that is already open —
// `I think` trailing off, or an answer in full — and a stub that answered every
// time would open a fresh window underneath them and hide the thing under test.
type rescueProviderSession struct {
	mu      sync.Mutex
	turn    rescueAITurn
	replied bool
}

// Start announces the session opening.
//
// Deliberately not configurable, and deliberately shaped like volc-duplex's: a
// synthetic ai.turn.end under bootstrapTurnID carrying an "ok" outcome, with no
// text delta. It is the same frame in all three providers, and the outcome is
// "ok" — which is exactly why the outcome guard cannot be what keeps it from
// arming the ladder.
func (s *rescueProviderSession) Start(_ context.Context, _ voiceproto.SessionStart, _ []ContinuationTurn) ([]ProviderOutbound, error) {
	return []ProviderOutbound{{
		Control: voiceproto.AITurnEnd{
			Type:    voiceproto.TypeAITurnEnd,
			TurnID:  bootstrapTurnID,
			Outcome: "ok",
		},
	}}, nil
}

// HandleClientControl answers the user's utterance with a real AI turn.
//
// This is what opens the silence window in production: the AI takes the floor,
// finishes, and the user is the one who owes the next move. The turn is named
// after the client's, which is what the provider does.
func (s *rescueProviderSession) HandleClientControl(_ context.Context, frameType string, data []byte) ([]ProviderOutbound, error) {
	if frameType != voiceproto.TypeUserSpeechEnd {
		return nil, nil
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	if s.replied {
		return nil, nil
	}
	s.replied = true

	var end voiceproto.UserSpeechEnd
	_ = json.Unmarshal(data, &end)
	turnID := strings.TrimSpace(end.TurnID)
	if turnID == "" {
		turnID = "stub-ai-turn"
	}
	out := []ProviderOutbound{
		{Control: voiceproto.NewAITextDelta("what would you say next?", turnID, 1)},
	}
	if s.turn.ttsEnd {
		out = append(out, ProviderOutbound{Control: voiceproto.AITTSEnd{
			Type:             voiceproto.TypeAITTSEnd,
			TurnID:           turnID,
			CompletionStatus: "ok",
		}})
	}
	if s.turn.turnEnd {
		out = append(out, ProviderOutbound{Control: voiceproto.AITurnEnd{
			Type:    voiceproto.TypeAITurnEnd,
			TurnID:  turnID,
			Outcome: s.turn.turnEndOutcome,
		}})
	}
	return out, nil
}

func (s *rescueProviderSession) HandleClientAudio(context.Context, []byte) ([]ProviderOutbound, error) {
	return nil, nil
}
func (s *rescueProviderSession) SnapshotUtterances() []EndUtterance { return nil }
func (s *rescueProviderSession) Close(context.Context) error        { return nil }

type rescueProvider struct {
	turn rescueAITurn
}

func (p *rescueProvider) Open(context.Context, ConsumedTicket, *SeqAllocator, *TurnRefAllocator) (VoiceProviderSession, error) {
	return &rescueProviderSession{turn: p.turn}, nil
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

// rescueAudioSynthesizer stands in for the TTS call. It returns a fixed amount
// of 16 kHz PCM: three frames' worth, so the emission path has something to
// chunk rather than one short frame that would hide a chunking bug.
type rescueAudioSynthesizer struct {
	mu     sync.Mutex
	texts  []string
	turns  []string
	pcm    []byte
	err    error
	voice  string
	frozen chan struct{} // when non-nil, Synthesize blocks until it is closed
}

func (a *rescueAudioSynthesizer) Synthesize(ctx context.Context, text, turnID string) (RescueAudio, error) {
	if a.frozen != nil {
		select {
		case <-a.frozen:
		case <-ctx.Done():
			return RescueAudio{}, ctx.Err()
		}
	}
	a.mu.Lock()
	defer a.mu.Unlock()
	a.texts = append(a.texts, text)
	a.turns = append(a.turns, turnID)
	if a.err != nil {
		return RescueAudio{}, a.err
	}
	pcm := a.pcm
	if pcm == nil {
		pcm = make([]byte, ClientAudioFormat.FrameBytes(ClientFrameMS)*3)
	}
	voice := a.voice
	if voice == "" {
		voice = "test-voice"
	}
	return RescueAudio{PCM: pcm, SampleRate: 16000, Codec: "pcm", VoiceID: voice}, nil
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
func newRescueRig(t *testing.T, turn rescueAITurn, synth *rescueAudioSynthesizer) *rescueRig {
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
		&rescueProvider{turn: turn},
		slog.New(slog.DiscardHandler),
		Options{InsecureSkipOrigin: true, RescueTick: rescueTestTick},
	)
	h.now = clock.now
	h.SetRescueComponents(
		NewSilenceDetectorWithThresholds(rescueTestThresholds()),
		NewRescueOrchestrator(gen, synthesizer, 0, slog.New(slog.DiscardHandler)),
	)

	mux := http.NewServeMux()
	h.Mount(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	return &rescueRig{handler: h, clock: clock, gen: gen, synth: synth, server: srv}
}

// connect completes the handshake and session.start, returning the connection
// and the frames the gateway pushed in reply.
//
// It stops once the session-open announcement has been read. That announcement
// opens no window — see TestHandler_Rescue_SessionOpenAnnouncementDoesNotOpenTheWindow —
// so every test that needs an armed ladder goes on to call speakTurn.
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

	// Read past the session-open announcement, so every caller starts from a
	// deterministic state instead of guessing how many frames it is.
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

// speakTurn drives one complete user utterance and waits for the stub's reply to
// finish, which is the moment the silence window opens.
//
// It is the production sequence, and the order inside it is what makes the
// window open: the user's complete answer closes whatever window was open, and
// the AI's own turn end — the first thing after it that says the AI stopped
// talking — opens the next one. Callers that want the user's *next* utterance to
// be the last thing that happened must not use this twice.
func (r *rescueRig) speakTurn(ctx context.Context, t *testing.T, conn *websocket.Conn, turnID string) {
	t.Helper()

	rescueSendFrame(ctx, t, conn, voiceproto.UserSpeechStart{Type: voiceproto.TypeUserSpeechStart})
	rescueSendFrame(ctx, t, conn, voiceproto.UserSpeechEnd{
		Type:   voiceproto.TypeUserSpeechEnd,
		Text:   "I would start with the migration window.",
		TurnID: turnID,
	})
	for {
		_, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("waiting for the AI reply to %s: %v", turnID, err)
		}
		var raw map[string]any
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Fatalf("decode frame: %v", err)
		}
		if raw["type"] == voiceproto.TypeAITTSEnd || raw["type"] == voiceproto.TypeAITurnEnd {
			return
		}
	}
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

// rescueReadAudioStream consumes one rung's audio: the ai.tts.start, its binary
// frames, and the ai.tts.end, returning the frames and the end marker.
//
// The end marker is returned rather than consumed because it is the only thing
// that says the rung's audio is over — a reader that swallows it desynchronises
// on the next rung, which is exactly what happened here: rung 2's ladder frame
// got skipped as "not the ai.tts.start I was looking for", and the test then read
// rung 1's leftover ladder instead.
//
// Interleaving is not hypothetical: the ladder frame and its audio are separate
// writes, so a later rung can begin while this one is still being spoken.
//
// start is taken as a parameter because it has already been read by the caller,
// and because re-reading it would consume frames the caller still needs.
func rescueReadAudioStream(
	ctx context.Context,
	t *testing.T,
	conn *websocket.Conn,
	start map[string]any,
) (frames []voiceproto.AITTSAudio, end map[string]any) {
	t.Helper()
	if got := start["type"]; got != voiceproto.TypeAITTSStart {
		t.Fatalf("expected %s, got %v", voiceproto.TypeAITTSStart, start)
	}
	layout := voiceproto.AudioFrameLayoutH4
	if _, attributed := start["turn_ref"]; attributed {
		layout = voiceproto.AudioFrameLayoutH8
	}
	for {
		typ, data, err := conn.Read(ctx)
		if err != nil {
			t.Fatalf("read during audio stream: %v", err)
		}
		if typ == websocket.MessageBinary {
			frame, err := voiceproto.DecodeAITTSAudio(data, layout)
			if err != nil {
				t.Fatalf("decode binary audio frame: %v", err)
			}
			frames = append(frames, frame)
			continue
		}
		var raw map[string]any
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Fatalf("decode frame: %v", err)
		}
		if raw["type"] == voiceproto.TypeAITTSEnd {
			return frames, raw
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

	rig := newRescueRig(t, rescueAITurn{ttsEnd: true, turnEnd: true}, &rescueAudioSynthesizer{})
	conn, _ := rig.connect(t)

	readCtx, cancel := context.WithTimeout(context.Background(), rescueTestWait)
	defer cancel()

	rig.speakTurn(readCtx, t, conn, "t-user-1")

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
		if got := frame["turn_id"]; got != "t-user-1" {
			t.Errorf("level %d turn_id = %v, want t-user-1", step.level, got)
		}
		// The rung is spoken through the client's existing TTS stream, not a URL
		// (docs/92): the frame stays text-only and the audio follows on the same
		// turn id, which is what makes the client play it at all.
		if got := frame["audio_url"]; got != "" {
			t.Errorf("level %d audio_url = %v, want empty — audio rides ai.tts.*", step.level, got)
		}
		start := rescueReadFrame(readCtx, t, conn)
		frames, end := rescueReadAudioStream(readCtx, t, conn, start)
		if got := start["turn_id"]; got != "t-user-1" {
			t.Errorf("level %d ai.tts.start turn_id = %v, want t-user-1", step.level, got)
		}
		if got := start["sample_rate"]; got != float64(16000) || start["codec"] != "pcm" {
			t.Errorf("level %d ai.tts.start = %#v, want 16000/pcm", step.level, start)
		}
		if len(frames) != 3 {
			t.Fatalf("level %d audio frames = %d, want 3", step.level, len(frames))
		}
		for i, audio := range frames {
			if len(audio.Payload) != ClientAudioFormat.FrameBytes(ClientFrameMS) {
				t.Errorf("level %d frame %d payload = %d bytes, want %d", step.level, i, len(audio.Payload), ClientAudioFormat.FrameBytes(ClientFrameMS))
			}
			if i > 0 && audio.Seq <= frames[i-1].Seq {
				t.Errorf("level %d frame %d seq = %d, want > %d", step.level, i, audio.Seq, frames[i-1].Seq)
			}
		}
		if got := end["completion_status"]; got != "ok" {
			t.Errorf("level %d ai.tts.end status = %v, want ok", step.level, got)
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

	rig := newRescueRig(t, rescueAITurn{ttsEnd: true, turnEnd: true}, nil)
	conn, _ := rig.connect(t)

	readCtx, cancel := context.WithTimeout(context.Background(), rescueTestWait)
	defer cancel()

	rig.speakTurn(readCtx, t, conn, "t-user-1")

	rig.clock.advance(rescueTestLevel1)
	rescueWaitForType(readCtx, t, conn, voiceproto.TypeRescueLadder)

	got := rig.gen.context()
	if got.ScenarioContext != "system design discussion" {
		t.Errorf("scenario = %q, want the session's scene_type", got.ScenarioContext)
	}
	if got.LastAIMessage != "what would you say next?" {
		t.Errorf("last AI message = %q, want the AI's own deltas", got.LastAIMessage)
	}
}

// A gateway with no audio path still delivers the prompt, and says so by leaving
// audio_url empty rather than inventing a URL iOS would fail to fetch.
func TestHandler_Rescue_WithoutSynthesizerEmitsTextOnlyLadder(t *testing.T) {
	t.Parallel()

	rig := newRescueRig(t, rescueAITurn{ttsEnd: true, turnEnd: true}, nil)
	conn, _ := rig.connect(t)

	readCtx, cancel := context.WithTimeout(context.Background(), rescueTestWait)
	defer cancel()

	rig.speakTurn(readCtx, t, conn, "t-user-1")

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

	rig := newRescueRig(t, rescueAITurn{ttsEnd: true}, nil)
	conn, _ := rig.connect(t)

	readCtx, cancel := context.WithTimeout(context.Background(), rescueTestWait)
	defer cancel()

	rig.speakTurn(readCtx, t, conn, "t-user-1")

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

	rig := newRescueRig(t, rescueAITurn{turnEnd: true}, nil)
	conn, _ := rig.connect(t)

	readCtx, cancel := context.WithTimeout(context.Background(), rescueTestWait)
	defer cancel()

	rig.speakTurn(readCtx, t, conn, "t-user-1")

	rig.clock.advance(rescueTestLevel1)
	frame := rescueWaitForType(readCtx, t, conn, voiceproto.TypeRescueLadder)
	if got := rescueLevelOf(t, frame); got != 1 {
		t.Fatalf("level = %d, want 1", got)
	}
}

// The frame every provider emits when the session opens is not a turn.
//
// It is a synthetic ai.turn.end that exists so the client can leave `aiSpeaking`,
// and it carries an "ok" outcome — so the outcome guard, which asks "did this
// turn ask the user something", lets it through. A window opened there arms the
// ladder before the AI has asked anything, and the user is prompted for a
// conversation that has not started.
func TestHandler_Rescue_SessionOpenAnnouncementDoesNotOpenTheWindow(t *testing.T) {
	t.Parallel()

	rig := newRescueRig(t, rescueAITurn{ttsEnd: true, turnEnd: true}, nil)
	conn, _ := rig.connect(t)

	rig.clock.advance(30 * time.Second)

	quietCtx, cancel := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancel()
	rescueExpectNoType(quietCtx, t, conn, voiceproto.TypeRescueLadder)

	if got := len(rig.gen.levels()); got != 0 {
		t.Errorf("generator ran %d times before the session had a turn, want 0", got)
	}
}

// A user who answers in full is not stuck, and must not be talked over. The
// clock is advanced far past every threshold afterwards, so the only thing that
// can keep the ladder away is the complete utterance having closed the window.
//
// The stub replies to the first utterance only, so the answer in full is the
// last thing that happens. Without that, the reply it would otherwise get would
// open a fresh window underneath the test and this would prove nothing.
func TestHandler_Rescue_CompleteAnswerIsNotRescued(t *testing.T) {
	t.Parallel()

	rig := newRescueRig(t, rescueAITurn{ttsEnd: true, turnEnd: true}, nil)
	conn, _ := rig.connect(t)

	ctx, cancel := context.WithTimeout(context.Background(), rescueTestWait)
	defer cancel()

	// The AI asks. This is what opens the window the answer below has to close.
	rig.speakTurn(ctx, t, conn, "t-ask-1")

	rescueSendFrame(ctx, t, conn, voiceproto.UserSpeechStart{Type: voiceproto.TypeUserSpeechStart})
	rescueSendFrame(ctx, t, conn, voiceproto.UserSpeechEnd{
		Type:   voiceproto.TypeUserSpeechEnd,
		Text:   "I agree with your point, the trade-off is worth it",
		TurnID: "t-answer-1",
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

	rig := newRescueRig(t, rescueAITurn{ttsEnd: true, turnEnd: true}, nil)
	conn, _ := rig.connect(t)

	ctx, cancel := context.WithTimeout(context.Background(), rescueTestWait)
	defer cancel()

	// An open window to talk over: without it the clock alone would keep the
	// ladder away and the test would pass for the wrong reason.
	rig.speakTurn(ctx, t, conn, "t-ask-1")

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

	rig := newRescueRig(t, rescueAITurn{ttsEnd: true, turnEnd: true}, nil)
	conn, _ := rig.connect(t)

	ctx, cancel := context.WithTimeout(context.Background(), rescueTestWait)
	defer cancel()

	// The AI asks, which starts the clock this test is about. The stub does not
	// reply again, so the trailing-off below is the last thing that happens.
	rig.speakTurn(ctx, t, conn, "t-ask-1")

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
		&rescueProvider{turn: rescueAITurn{ttsEnd: true, turnEnd: true}},
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

	rig := newRescueRig(t, rescueAITurn{ttsEnd: true, turnEnd: true}, nil)

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

	rig := newRescueRig(t, rescueAITurn{ttsEnd: true, turnEnd: true}, nil)
	conn, _ := rig.connect(t)

	ctx, cancel := context.WithTimeout(context.Background(), rescueTestWait)
	defer cancel()

	// An open window to resume into, and a stub that will not speak again.
	rig.speakTurn(ctx, t, conn, "t-ask-1")

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
//
// The turn does speak here — the stub answers and only its *outcome* is a
// failure — so the outcome is the only thing keeping the window shut. A stub
// that stayed silent would leave this test passing on the turn-never-spoke
// guard instead, and the rule it exists to pin would go untested.
func TestHandler_Rescue_FailedTurnDoesNotOpenWindow(t *testing.T) {
	t.Parallel()

	for _, outcome := range []string{voiceproto.TurnOutcomeTimeout, voiceproto.TurnOutcomeError} {
		t.Run(outcome, func(t *testing.T) {
			t.Parallel()

			rig := newRescueRig(t, rescueAITurn{turnEnd: true, turnEndOutcome: outcome}, nil)
			conn, _ := rig.connect(t)

			ctx, cancel := context.WithTimeout(context.Background(), rescueTestWait)
			defer cancel()
			rig.speakTurn(ctx, t, conn, "t-ask-1")

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

	rig := newRescueRig(t, rescueAITurn{turnEnd: true, turnEndOutcome: voiceproto.TurnOutcomePartial}, nil)
	conn, _ := rig.connect(t)

	readCtx, cancel := context.WithTimeout(context.Background(), rescueTestWait)
	defer cancel()

	rig.speakTurn(readCtx, t, conn, "t-ask-1")

	rig.clock.advance(rescueTestLevel1)
	frame := rescueWaitForType(readCtx, t, conn, voiceproto.TypeRescueLadder)
	if got := rescueLevelOf(t, frame); got != 1 {
		t.Fatalf("level = %d, want 1", got)
	}
}

// A rung that keeps talking after the learner starts talking is worse than no
// rung at all: the ladder exists to get them speaking, so the moment they do is
// exactly when it should stop. The client stops on ai.tts.end, so the stop has to
// be a frame.
//
// The rung has to be *in pieces* for this to prove anything. A 3-second rung is
// written in one uninterrupted loop of in-memory writes, so a test that sends
// "the user spoke" afterwards finds the rung already finished and every frame
// already spent — it would pass on a gateway that never interrupts at all.
// Reading one frame at a time makes the user's turn arrive while frames are
// still owed, which is the situation being tested.
func TestHandler_Rescue_UserSpeakingInterruptsTheSpokenLadder(t *testing.T) {
	t.Parallel()

	const total = 30
	synth := &rescueAudioSynthesizer{pcm: make([]byte, ClientAudioFormat.FrameBytes(ClientFrameMS)*total)}
	rig := newRescueRig(t, rescueAITurn{ttsEnd: true, turnEnd: true}, synth)
	conn, _ := rig.connect(t)

	readCtx, cancel := context.WithTimeout(context.Background(), rescueTestWait)
	defer cancel()

	rig.speakTurn(readCtx, t, conn, "t-ask-1")

	rig.clock.advance(rescueTestLevel1)
	rescueWaitForType(readCtx, t, conn, voiceproto.TypeRescueLadder)
	start := rescueWaitForType(readCtx, t, conn, voiceproto.TypeAITTSStart)
	startRef, attributed := start["turn_ref"]
	if !attributed {
		t.Fatalf("the rung's ai.tts.start carries no turn_ref, so its frames cannot be attributed: %#v", start)
	}

	// Read a couple of frames so the stream is demonstrably under way.
	frames := 0
	for frames < 2 {
		typ, _, err := conn.Read(readCtx)
		if err != nil {
			t.Fatalf("read while audio was owed: %v", err)
		}
		if typ == websocket.MessageBinary {
			frames++
		}
	}

	// Speak. Everything still owed must be dropped in favour of the learner.
	rescueSendFrame(readCtx, t, conn, voiceproto.UserSpeechStart{Type: voiceproto.TypeUserSpeechStart})
	rescueSendFrame(readCtx, t, conn, voiceproto.UserSpeechEnd{
		Type: voiceproto.TypeUserSpeechEnd,
		Text: "I think the main risk is the migration window.",
	})

	deadline := time.Now().Add(rescueTestWait)
	for {
		if time.Now().After(deadline) {
			t.Fatalf("no ai.tts.end after the user spoke; %d frames were read", frames)
		}
		typ, data, err := conn.Read(readCtx)
		if err != nil {
			t.Fatalf("read after the user spoke: %v", err)
		}
		if typ == websocket.MessageBinary {
			frames++
			continue
		}
		var raw map[string]any
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Fatalf("decode frame: %v", err)
		}
		if raw["type"] != voiceproto.TypeAITTSEnd {
			continue
		}
		if got := raw["completion_status"]; got != "interrupted" {
			t.Fatalf("ai.tts.end status = %v, want interrupted once the user spoke", got)
		}
		if got := raw["turn_ref"]; got != startRef {
			t.Fatalf("interrupting end turn_ref = %#v, want %#v (the value its start announced)", got, startRef)
		}
		if frames >= total {
			t.Fatalf("all %d frames were spent before the interrupt: the rung finished first, so this proved nothing", frames)
		}
		return
	}
}

// The user's own request skips the threshold but not the ladder: it is answered
// immediately, with the rung the automatic path would have reached next.
func TestHandler_Rescue_UserRequestAnswersWithoutWaitingForTheThreshold(t *testing.T) {
	t.Parallel()

	rig := newRescueRig(t, rescueAITurn{ttsEnd: true, turnEnd: true}, &rescueAudioSynthesizer{})
	conn, _ := rig.connect(t)

	readCtx, cancel := context.WithTimeout(context.Background(), rescueTestWait)
	defer cancel()

	rig.speakTurn(readCtx, t, conn, "t-user-1")

	// The clock has not moved, so the automatic ladder is not due for another
	// rescueTestLevel1. Anything that arrives now can only be the request.
	rescueSendFrame(readCtx, t, conn, voiceproto.ClientRescueRequest{Type: voiceproto.TypeClientRescueRequest})

	frame := rescueWaitForType(readCtx, t, conn, voiceproto.TypeRescueLadder)
	if got := rescueLevelOf(t, frame); got != 1 {
		t.Fatalf("level = %d, want 1 (%#v)", got, frame)
	}
	if got := frame["turn_id"]; got != "t-user-1" {
		t.Errorf("turn_id = %v, want t-user-1", got)
	}
}

// A request consumes a rung, so the automatic ladder must continue past it
// rather than answer the same silence a second time.
func TestHandler_Rescue_UserRequestAdvancesTheAutomaticLadderRatherThanOpeningASecond(t *testing.T) {
	t.Parallel()

	rig := newRescueRig(t, rescueAITurn{ttsEnd: true, turnEnd: true}, &rescueAudioSynthesizer{})
	conn, _ := rig.connect(t)

	readCtx, cancel := context.WithTimeout(context.Background(), rescueTestWait)
	defer cancel()

	rig.speakTurn(readCtx, t, conn, "t-user-1")
	rescueSendFrame(readCtx, t, conn, voiceproto.ClientRescueRequest{Type: voiceproto.TypeClientRescueRequest})

	first := rescueWaitForType(readCtx, t, conn, voiceproto.TypeRescueLadder)
	if got := rescueLevelOf(t, first); got != 1 {
		t.Fatalf("request answered with level %d, want 1", got)
	}
	start := rescueReadFrame(readCtx, t, conn)
	rescueReadAudioStream(readCtx, t, conn, start)

	// The automatic ladder's own level-2 threshold elapses. It must continue
	// past the rung the request spent rather than answer the same silence again.
	rig.clock.advance(rescueTestLevel2)
	second := rescueWaitForType(readCtx, t, conn, voiceproto.TypeRescueLadder)
	if got := rescueLevelOf(t, second); got != 2 {
		t.Fatalf("level = %d, want 2 (%#v)", got, second)
	}

	// The generator is the outside view of "which rungs were asked for": a
	// duplicate level 1 would show up here even if the frame looked right.
	want := []conversation.RescueLevel{conversation.RescueSkeleton, conversation.RescueHint}
	if got := rig.gen.levels(); !slices.Equal(got, want) {
		t.Fatalf("generator levels = %v, want %v", got, want)
	}
}

// The request is answered before the AI has handed the floor over, so there is
// nothing to be rescued from. Refusing is silent on purpose: an error frame maps
// to iOS .failed and would kill the session over a tap.
func TestHandler_Rescue_UserRequestBeforeAnyAITurnIsRefusedSilently(t *testing.T) {
	t.Parallel()

	rig := newRescueRig(t, rescueAITurn{ttsEnd: true, turnEnd: true}, &rescueAudioSynthesizer{})
	conn, _ := rig.connect(t)

	readCtx, cancel := context.WithTimeout(context.Background(), rescueTestWait)
	defer cancel()

	rescueSendFrame(readCtx, t, conn, voiceproto.ClientRescueRequest{Type: voiceproto.TypeClientRescueRequest})

	quietCtx, cancelQuiet := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancelQuiet()
	for {
		_, data, err := conn.Read(quietCtx)
		if err != nil {
			return
		}
		var raw map[string]any
		if err := json.Unmarshal(data, &raw); err != nil {
			t.Fatalf("decode frame: %v", err)
		}
		t.Fatalf("a request the gateway could not serve was answered with %#v", raw)
	}
}

// With B8 unwired the frame must be inert: the gateway answers nothing, and the
// session survives it exactly as it survives any other frame it does not know.
func TestHandler_Rescue_UserRequestIsInertWhenRescueIsNotWired(t *testing.T) {
	t.Parallel()

	clock := newRescueTestClock()
	h := NewHandler(
		rescueTicketConsumer{},
		rescueLifecycle{},
		&rescueProvider{turn: rescueAITurn{ttsEnd: true, turnEnd: true}},
		slog.New(slog.DiscardHandler),
		Options{InsecureSkipOrigin: true, RescueTick: rescueTestTick},
	)
	h.now = clock.now

	mux := http.NewServeMux()
	h.Mount(mux)
	srv := httptest.NewServer(mux)
	t.Cleanup(srv.Close)

	rig := &rescueRig{handler: h, clock: clock, gen: &rescueTextGenerator{}, server: srv}
	conn, _ := rig.connect(t)

	readCtx, cancel := context.WithTimeout(context.Background(), rescueTestWait)
	defer cancel()

	rig.speakTurn(readCtx, t, conn, "t-user-1")
	rescueSendFrame(readCtx, t, conn, voiceproto.ClientRescueRequest{Type: voiceproto.TypeClientRescueRequest})

	clock.advance(10 * time.Minute)

	quietCtx, cancelQuiet := context.WithTimeout(context.Background(), 300*time.Millisecond)
	defer cancelQuiet()
	rescueExpectNoType(quietCtx, t, conn, voiceproto.TypeRescueLadder)
}
