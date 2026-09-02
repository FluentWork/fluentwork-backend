// Package main runs live WSS round-trip integration scripts for the
// B14 server-side ASR fallback path. It does NOT spawn the production
// voice-gateway process or depend on app-server / MySQL / Redis: instead it
// boots the production Handler on an httptest server and drives it with a
// real WebSocket client (coder/websocket) so the assertions exercise the
// full write/encode/decode/read path.
//
// Scenarios (selected via --scenario flag):
//
//	client-asr    — iOS sends user.speech.end(text=non-empty). Handler must
//	                drive the badge emitter with the client text and emit a
//	                feedback.badge frame on a phrase-block match.
//	server-asr    — iOS sends user.speech.end(text=""). Provider (mock with
//	                ServerASRText set) returns ClientASRTranscription +
//	                ServerASRText. Handler must fall back to ServerASRText
//	                for badge detection.
//	relay         — Verify the gateway emits client.asr.transcription
//	                WSS frames to the client so iOS ServerRelayASRTranscriber
//	                receives the authoritative transcript.
package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"strings"
	"time"

	"github.com/coder/websocket"

	"github.com/FluentWork/fluentwork-backend/internal/session"
	"github.com/FluentWork/fluentwork-backend/internal/voicegateway"
	"github.com/FluentWork/fluentwork-backend/internal/voiceproto"
)

type scenario string

const (
	scenarioClientASR scenario = "client-asr"
	scenarioServerASR scenario = "server-asr"
	scenarioRelay     scenario = "relay"
)

func main() {
	if err := run(); err != nil {
		fmt.Fprintf(os.Stderr, "integration FAILED: %v\n", err)
		os.Exit(1)
	}
}

func run() error {
	var (
		scen        = flag.String("scenario", "", "one of: client-asr, server-asr, relay")
		wsURL       = flag.String("ws-url", "", "override ws URL (default: spin up httptest server)")
		turnText    = flag.String("turn-text", "client-asr-text", "client text sent on user.speech.end")
		providerASR = flag.String("provider-server-asr-text", "", "ServerASRText the provider returns (server-asr / relay scenarios)")
		phrase      = flag.String("phrase", "", "phrase block expression (default: derived from --turn-text)")
		timeoutMS   = flag.Int("timeout-ms", 5000, "overall timeout in milliseconds")
	)
	flag.Parse()

	if *scen == "" {
		return errors.New("--scenario is required")
	}
	s := scenario(*scen)
	if s != scenarioClientASR && s != scenarioServerASR && s != scenarioRelay {
		return fmt.Errorf("unknown scenario %q (want client-asr|server-asr|relay)", *scen)
	}

	ctx, cancel := context.WithTimeout(context.Background(), time.Duration(*timeoutMS)*time.Millisecond)
	defer cancel()

	if *phrase == "" {
		*phrase = *turnText
	}

	var srv *httptest.Server
	if *wsURL == "" {
		srv = newGatewayForScenario(s, *providerASR)
		defer srv.Close()
	}

	target := *wsURL
	if srv != nil {
		target = "ws" + strings.TrimPrefix(srv.URL, "http") + "/v1/voice"
	}

	evidence := map[string]any{
		"scenario":            string(s),
		"ws_url":              target,
		"turn_text":           *turnText,
		"provider_server_asr": *providerASR,
	}

	conn, _, err := websocket.Dial(ctx, target, nil)
	if err != nil {
		return fmt.Errorf("Dial: %w", err)
	}
	defer func() { _ = conn.Close(websocket.StatusNormalClosure, "") }()

	if err := writeFrame(ctx, conn, voiceproto.Auth{Type: voiceproto.TypeAuth, Ticket: "good-ticket"}); err != nil {
		return fmt.Errorf("write auth: %w", err)
	}
	if _, err := readOneFrame(ctx, conn); err != nil {
		return fmt.Errorf("read session.ready: %w", err)
	}

	if err := writeFrame(ctx, conn, voiceproto.SessionStart{Type: voiceproto.TypeSessionStart}); err != nil {
		return fmt.Errorf("write session.start: %w", err)
	}
	// Drain the Start() frames (ai.text.delta + ai.turn.end for the stub provider).
	for {
		f, err := readOneFrame(ctx, conn)
		if err != nil {
			return fmt.Errorf("drain start frames: %w", err)
		}
		if t, _ := f["type"].(string); t == voiceproto.TypeAITurnEnd {
			break
		}
	}

	if err := writeFrame(ctx, conn, voiceproto.UserSpeechEnd{
		Type:   voiceproto.TypeUserSpeechEnd,
		Text:   *turnText,
		TurnID: "turn-integration-1",
	}); err != nil {
		return fmt.Errorf("write user.speech.end: %w", err)
	}

	var (
		gotRelay bool
		gotBadge bool
		relayTxt string
	)
	for {
		frame, err := readOneFrame(ctx, conn)
		if err != nil {
			// Read deadline is fine if we already collected what we needed.
			if errors.Is(err, context.DeadlineExceeded) || errors.Is(err, io.EOF) {
				break
			}
			return fmt.Errorf("read post user.speech.end: %w", err)
		}
		switch frame["type"] {
		case voiceproto.TypeClientASRTranscription:
			gotRelay = true
			if t, _ := frame["text"].(string); t != "" {
				relayTxt = t
			}
		case voiceproto.TypeFeedbackBadge:
			gotBadge = true
		}
		// Keep draining until either we have both relay + badge, or we
		// timeout / see a non-relevant frame.
		if gotRelay && gotBadge {
			// Final answer for relay+badge scenarios; for client-asr we
			// still want to drain possible follow-ups.
			if s == scenarioServerASR {
				break
			}
		}
	}

	evidence["got_relay"] = gotRelay
	evidence["relay_text"] = relayTxt
	evidence["got_badge"] = gotBadge

	switch s {
	case scenarioClientASR:
		if !gotBadge {
			return fmt.Errorf("client-asr scenario: expected feedback.badge on hit, got none")
		}
		if gotRelay {
			return fmt.Errorf("client-asr scenario: did not expect client.asr.transcription (no provider server ASR configured)")
		}
	case scenarioServerASR:
		if !gotRelay {
			return fmt.Errorf("server-asr scenario: expected client.asr.transcription relay")
		}
		if relayTxt != *providerASR {
			return fmt.Errorf("server-asr scenario: relay text %q != configured provider ASR %q", relayTxt, *providerASR)
		}
		if !gotBadge {
			return fmt.Errorf("server-asr scenario: expected feedback.badge (server ASR should drive detection)")
		}
	case scenarioRelay:
		if !gotRelay {
			return fmt.Errorf("relay scenario: expected client.asr.transcription relay")
		}
		if relayTxt != *providerASR {
			return fmt.Errorf("relay scenario: relay text %q != configured provider ASR %q", relayTxt, *providerASR)
		}
	}

	// Pretty-print evidence so the wrapper script can capture it.
	enc := json.NewEncoder(os.Stdout)
	enc.SetIndent("", "  ")
	if err := enc.Encode(evidence); err != nil {
		return fmt.Errorf("emit evidence: %w", err)
	}
	return nil
}

func writeFrame(ctx context.Context, conn *websocket.Conn, payload any) error {
	raw := voiceproto.MustMarshal(payload)
	return conn.Write(ctx, websocket.MessageText, raw)
}

func readOneFrame(ctx context.Context, conn *websocket.Conn) (map[string]any, error) {
	_, data, err := conn.Read(ctx)
	if err != nil {
		return nil, err
	}
	var out map[string]any
	if err := json.Unmarshal(data, &out); err != nil {
		return nil, fmt.Errorf("unmarshal frame %q: %w", string(data), err)
	}
	return out, nil
}

// newGatewayForScenario boots the production voicegateway.Handler with a
// stub provider + stub consumer + stub lifecycle + an in-memory HitDetector
// seeded with the phrase block. The provider stub either:
//   - has ServerASRText empty (client-asr scenario: no relay, client text drives detection)
//   - has ServerASRText set   (server-asr / relay scenarios: relay frame + badge via fallback)
func newGatewayForScenario(s scenario, providerASR string) *httptest.Server {
	consumer := &integrationConsumer{ticket: "good-ticket", out: voicegateway.ConsumedTicket{TicketID: "t1", SessionID: "s1", UserID: "u1"}}
	provider := &integrationProvider{session: &integrationProviderSession{serverASRText: providerASR}}
	src := newInMemoryBlockSource(session.BlockCandidate{ID: "block-int-1", ExpressionEN: derivedPhraseFor(s, providerASR)})
	det := session.NewHitDetector(src)
	badgeEmitter := voicegateway.NewBadgeEmitterForTest(det, nil, voicegateway.BadgeEmitterOptions{})
	h := voicegateway.NewHandler(consumer, &integrationLifecycle{}, provider, nil, voicegateway.Options{InsecureSkipOrigin: true})
	if badgeEmitter != nil {
		h.SetBadgeEmitter(badgeEmitter)
	}
	mux := http.NewServeMux()
	h.Mount(mux)
	return httptest.NewServer(mux)
}

func derivedPhraseFor(s scenario, providerASR string) string {
	if providerASR != "" {
		return providerASR
	}
	return "client-asr-text"
}

type integrationConsumer struct {
	ticket string
	out    voicegateway.ConsumedTicket
}

func (c *integrationConsumer) Consume(_ context.Context, raw string) (voicegateway.ConsumedTicket, error) {
	if raw != c.ticket {
		return voicegateway.ConsumedTicket{}, errors.New("invalid ticket")
	}
	return c.out, nil
}

type integrationLifecycle struct{}

func (*integrationLifecycle) Activate(_ context.Context, _ string) error { return nil }
func (*integrationLifecycle) End(_ context.Context, _ voicegateway.EndSessionRequest) error {
	return nil
}

type integrationProvider struct{ session *integrationProviderSession }

func (p *integrationProvider) Open(_ context.Context, _ voicegateway.ConsumedTicket) (voicegateway.VoiceProviderSession, error) {
	return p.session, nil
}

type integrationProviderSession struct {
	serverASRText string
}

func (s *integrationProviderSession) Start(_ context.Context, _ voiceproto.SessionStart) ([]voicegateway.ProviderOutbound, error) {
	return []voicegateway.ProviderOutbound{
		{Control: map[string]any{
			"type": voiceproto.TypeAITextDelta,
			"text": "ready",
		}},
		{Control: voiceproto.AITurnEnd{Type: voiceproto.TypeAITurnEnd}},
	}, nil
}

func (s *integrationProviderSession) HandleClientControl(_ context.Context, _ string, _ []byte) ([]voicegateway.ProviderOutbound, error) {
	if s.serverASRText == "" {
		return nil, nil
	}
	return []voicegateway.ProviderOutbound{
		{
			Control: voiceproto.ClientASRTranscription{
				Type:   voiceproto.TypeClientASRTranscription,
				Text:   s.serverASRText,
				TurnID: "int-turn-1",
			},
			ServerASRText: s.serverASRText,
		},
	}, nil
}

func (s *integrationProviderSession) HandleClientAudio(_ context.Context, _ []byte) ([]voicegateway.ProviderOutbound, error) {
	return nil, nil
}

func (s *integrationProviderSession) SnapshotUtterances() []voicegateway.EndUtterance { return nil }
func (s *integrationProviderSession) Close(_ context.Context) error                   { return nil }

type inMemoryBlockSource struct {
	candidates []session.BlockCandidate
}

func newInMemoryBlockSource(c ...session.BlockCandidate) *inMemoryBlockSource {
	return &inMemoryBlockSource{candidates: c}
}

func (s *inMemoryBlockSource) CandidatesForUser(_ context.Context, _ string) ([]session.BlockCandidate, error) {
	out := make([]session.BlockCandidate, len(s.candidates))
	copy(out, s.candidates)
	return out, nil
}
