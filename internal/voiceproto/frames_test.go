package voiceproto_test

import (
	"encoding/json"
	"strings"
	"testing"

	"github.com/FluentWork/fluentwork-backend/internal/voiceproto"
	sharedschemas "github.com/FluentWork/fluentwork-backend/schemas"
)

func TestControlFrameRoundTrip(t *testing.T) {
	t.Parallel()

	cases := []any{
		voiceproto.Auth{Type: voiceproto.TypeAuth, Ticket: "abc"},
		voiceproto.SessionReady{Type: voiceproto.TypeSessionReady, SessionID: "s1", UserID: "u1"},
		voiceproto.SessionStart{Type: voiceproto.TypeSessionStart, SceneType: "demo"},
		voiceproto.AITurnEnd{Type: voiceproto.TypeAITurnEnd, TurnID: "turn-1"},
		voiceproto.AITTSStart{
			Type:       voiceproto.TypeAITTSStart,
			TurnID:     "turn-9",
			VoiceID:    "mock_voice_01",
			SampleRate: 24000,
			Codec:      "opus",
		},
		voiceproto.AITTSEnd{
			Type:             voiceproto.TypeAITTSEnd,
			TurnID:           "turn-9",
			CompletionStatus: "ok",
		},
		voiceproto.Interrupt{Type: voiceproto.TypeInterrupt},
		voiceproto.SessionEnd{Type: voiceproto.TypeSessionEnd, Reason: "user"},
		voiceproto.ErrorFrame{Type: voiceproto.TypeError, Code: "unauthenticated", Message: "bad ticket"},
		voiceproto.Ping{Type: voiceproto.TypePing, TS: 1},
		voiceproto.Pong{Type: voiceproto.TypePong, TS: 1},
		voiceproto.FeedbackBadge{
			Type:          voiceproto.TypeFeedbackBadge,
			Badge:         "fluency+1",
			PhraseBlockID: "block-1",
			Tier:          voiceproto.BadgeTierSoft,
			SessionID:     "s1",
			TurnID:        "turn-1",
			DedupeKey:     "s1|turn-1|block-1",
		},
	}
	for _, c := range cases {
		raw, err := json.Marshal(c)
		if err != nil {
			t.Fatalf("marshal: %v", err)
		}
		typ, err := voiceproto.DecodeType(raw)
		if err != nil {
			t.Fatalf("DecodeType(%s): %v", raw, err)
		}
		if typ == "" {
			t.Fatalf("empty type for %s", raw)
		}
	}
}

func TestSchemaFilePresent(t *testing.T) {
	t.Parallel()
	var doc map[string]any
	if err := json.Unmarshal(sharedschemas.WSSControlFramesV1, &doc); err != nil {
		t.Fatalf("schema json: %v", err)
	}
	if doc["title"] == nil {
		t.Fatal("schema missing title")
	}
	defs, ok := doc["$defs"].(map[string]any)
	if !ok {
		t.Fatal("schema missing $defs")
	}
	for _, name := range []string{"auth", "sessionReady", "sessionStart", "aiTurnEnd", "sessionEnd", "interrupt", "error", "ping", "pong", "feedbackBadge"} {
		if _, ok := defs[name]; !ok {
			t.Fatalf("schema missing $defs.%s", name)
		}
	}
}

func TestSchemaV2AddsTTSFrames(t *testing.T) {
	t.Parallel()
	var doc map[string]any
	if err := json.Unmarshal(sharedschemas.WSSControlFramesV2, &doc); err != nil {
		t.Fatalf("schema json: %v", err)
	}
	defs, ok := doc["$defs"].(map[string]any)
	if !ok {
		t.Fatal("schema missing $defs")
	}
	for _, name := range []string{"aiTTSStart", "aiTTSAudio", "aiTTSEnd"} {
		if _, ok := defs[name]; !ok {
			t.Fatalf("v2 schema missing $defs.%s", name)
		}
	}
	oneOf, ok := doc["oneOf"].([]any)
	if !ok {
		t.Fatal("schema missing oneOf")
	}
	refs := map[string]bool{}
	for _, item := range oneOf {
		m, _ := item.(map[string]any)
		ref, _ := m["$ref"].(string)
		refs[ref] = true
	}
	if !refs["#/$defs/aiTTSStart"] || !refs["#/$defs/aiTTSEnd"] {
		t.Fatalf("v2 oneOf missing ai.tts start/end: %#v", refs)
	}
	if refs["#/$defs/aiTTSAudio"] {
		t.Fatal("aiTTSAudio must not be in JSON control oneOf; it is a binary message")
	}
}

func TestAITurnEndJSONCarriesOutcomeAndLogID(t *testing.T) {
	t.Parallel()

	frame := voiceproto.AITurnEnd{
		Type:    voiceproto.TypeAITurnEnd,
		TurnID:  "turn-timeout-1",
		Outcome: "timeout",
		LogID:   "volc-log-abc",
	}
	raw, err := json.Marshal(frame)
	if err != nil {
		t.Fatalf("marshal: %v", err)
	}
	var asMap map[string]any
	if err := json.Unmarshal(raw, &asMap); err != nil {
		t.Fatalf("unmarshal map: %v", err)
	}
	if asMap["type"] != voiceproto.TypeAITurnEnd {
		t.Fatalf("type = %#v", asMap["type"])
	}
	if asMap["outcome"] != "timeout" {
		t.Fatalf("outcome missing on wire: %s", raw)
	}
	if asMap["log_id"] != "volc-log-abc" {
		t.Fatalf("log_id missing on wire: %s", raw)
	}

	var decoded voiceproto.AITurnEnd
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal: %v", err)
	}
	if decoded != frame {
		t.Fatalf("round trip: %+v", decoded)
	}

	omitted, err := json.Marshal(voiceproto.AITurnEnd{Type: voiceproto.TypeAITurnEnd, TurnID: "t1"})
	if err != nil {
		t.Fatalf("marshal omitted: %v", err)
	}
	if strings.Contains(string(omitted), `"outcome"`) || strings.Contains(string(omitted), `"log_id"`) {
		t.Fatalf("empty outcome/log_id must omit: %s", omitted)
	}
}

func TestSchemaAITurnEndIncludesOutcomeAndLogID(t *testing.T) {
	t.Parallel()

	for _, raw := range [][]byte{sharedschemas.WSSControlFramesV1, sharedschemas.WSSControlFramesV2} {
		var doc map[string]any
		if err := json.Unmarshal(raw, &doc); err != nil {
			t.Fatalf("schema json: %v", err)
		}
		defs, ok := doc["$defs"].(map[string]any)
		if !ok {
			t.Fatal("schema missing $defs")
		}
		aiTurnEnd, ok := defs["aiTurnEnd"].(map[string]any)
		if !ok {
			t.Fatal("schema missing $defs.aiTurnEnd")
		}
		props, ok := aiTurnEnd["properties"].(map[string]any)
		if !ok {
			t.Fatal("aiTurnEnd missing properties")
		}
		if _, ok := props["outcome"]; !ok {
			t.Fatal("aiTurnEnd schema missing outcome")
		}
		if _, ok := props["log_id"]; !ok {
			t.Fatal("aiTurnEnd schema missing log_id")
		}
	}
}

func TestAITTSStartEndJSONRoundTrip(t *testing.T) {
	t.Parallel()
	start := voiceproto.AITTSStart{
		Type:       voiceproto.TypeAITTSStart,
		TurnID:     "turn-1",
		VoiceID:    "mock_voice_01",
		SampleRate: 24000,
		Codec:      "opus",
	}
	raw, err := json.Marshal(start)
	if err != nil {
		t.Fatalf("marshal start: %v", err)
	}
	typ, err := voiceproto.DecodeType(raw)
	if err != nil || typ != voiceproto.TypeAITTSStart {
		t.Fatalf("DecodeType start: %q %v", typ, err)
	}
	var decoded voiceproto.AITTSStart
	if err := json.Unmarshal(raw, &decoded); err != nil {
		t.Fatalf("unmarshal start: %v", err)
	}
	if decoded != start {
		t.Fatalf("start round trip: %+v", decoded)
	}

	end := voiceproto.AITTSEnd{
		Type:             voiceproto.TypeAITTSEnd,
		TurnID:           "turn-1",
		CompletionStatus: "interrupted",
	}
	raw, err = json.Marshal(end)
	if err != nil {
		t.Fatalf("marshal end: %v", err)
	}
	if typ, err = voiceproto.DecodeType(raw); err != nil || typ != voiceproto.TypeAITTSEnd {
		t.Fatalf("DecodeType end: %q %v", typ, err)
	}
	var decodedEnd voiceproto.AITTSEnd
	if err := json.Unmarshal(raw, &decodedEnd); err != nil {
		t.Fatalf("unmarshal end: %v", err)
	}
	if decodedEnd.DurationMs != nil {
		t.Fatal("optional duration_ms should omit")
	}
	ms := 200
	end.DurationMs = &ms
	raw, err = json.Marshal(end)
	if err != nil {
		t.Fatalf("marshal end with duration: %v", err)
	}
	if err := json.Unmarshal(raw, &decodedEnd); err != nil {
		t.Fatalf("unmarshal end with duration: %v", err)
	}
	if decodedEnd.DurationMs == nil || *decodedEnd.DurationMs != 200 {
		t.Fatalf("duration_ms = %#v", decodedEnd.DurationMs)
	}
}

func TestAITTSAudioBinaryRoundTrip(t *testing.T) {
	t.Parallel()
	frame := voiceproto.AITTSAudio{Seq: 9, Payload: []byte{0x0A, 0x0B}}
	raw, err := frame.Encode()
	if err != nil {
		t.Fatalf("encode: %v", err)
	}
	got, err := voiceproto.DecodeAITTSAudio(raw)
	if err != nil {
		t.Fatalf("decode: %v", err)
	}
	if got.Seq != 9 || string(got.Payload) != string(frame.Payload) {
		t.Fatalf("got %+v", got)
	}
	if _, err := (voiceproto.AITTSAudio{Seq: 0, Payload: nil}).Encode(); err == nil {
		t.Fatal("empty payload must fail")
	}
	if _, err := voiceproto.DecodeAITTSAudio([]byte{0, 0, 0, 1}); err == nil {
		t.Fatal("truncated payload must fail")
	}
}

func TestAITTSAudioJSONTypeIsNotAControlPayload(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"type":"ai.tts.audio","turn_id":"turn-1","seq":0,"data":"AAEC"}`)
	typ, err := voiceproto.DecodeType(raw)
	if err != nil || typ != voiceproto.TypeAITTSAudio {
		t.Fatalf("DecodeType: %q %v", typ, err)
	}
}

func TestControlFrameTypeConstantsAreUnique(t *testing.T) {
	t.Parallel()
	types := []string{
		voiceproto.TypeAuth,
		voiceproto.TypeSessionReady,
		voiceproto.TypeSessionStart,
		voiceproto.TypeUserSpeechStart,
		voiceproto.TypeUserSpeechEnd,
		voiceproto.TypeClientASRTranscription,
		voiceproto.TypeAITextDelta,
		voiceproto.TypeAIAudioChunk,
		voiceproto.TypeAITTSStart,
		voiceproto.TypeAITTSAudio,
		voiceproto.TypeAITTSEnd,
		voiceproto.TypeAITurnEnd,
		voiceproto.TypeInterrupt,
		voiceproto.TypeFeedbackBadge,
		voiceproto.TypeSessionEnd,
		voiceproto.TypeError,
		voiceproto.TypePong,
		voiceproto.TypePing,
	}
	seen := map[string]bool{}
	for _, typ := range types {
		if seen[typ] {
			t.Fatalf("duplicate type %q", typ)
		}
		seen[typ] = true
	}
}

func TestFeedbackBadgeDedupeKey(t *testing.T) {
	t.Parallel()
	got := voiceproto.ComposeBadgeDedupeKey("s1", "turn-1", "block-1")
	want := "s1|turn-1|block-1"
	if got != want {
		t.Fatalf("ComposeBadgeDedupeKey: got %q want %q", got, want)
	}
	// Missing fields produce empty key (caller must skip emission).
	if key := voiceproto.ComposeBadgeDedupeKey("", "turn-1", "block-1"); key != "" {
		t.Fatalf("missing session should yield empty key, got %q", key)
	}
	if key := voiceproto.ComposeBadgeDedupeKey("s1", "", "block-1"); key != "" {
		t.Fatalf("missing turn should yield empty key, got %q", key)
	}
	if key := voiceproto.ComposeBadgeDedupeKey("s1", "turn-1", " "); key != "" {
		t.Fatalf("blank phrase_block should yield empty key, got %q", key)
	}
}

func TestNewFeedbackBadgePopulatesFields(t *testing.T) {
	t.Parallel()
	badge := voiceproto.NewFeedbackBadge(
		"fluency+1", "block-1", voiceproto.BadgeTierSoft, "s1", "turn-1",
	)
	if badge.Type != voiceproto.TypeFeedbackBadge {
		t.Fatalf("Type: got %q want %q", badge.Type, voiceproto.TypeFeedbackBadge)
	}
	if badge.Badge != "fluency+1" {
		t.Fatalf("Badge: got %q want %q", badge.Badge, "fluency+1")
	}
	if badge.DedupeKey != "s1|turn-1|block-1" {
		t.Fatalf("DedupeKey: got %q", badge.DedupeKey)
	}
}

func TestFeedbackBadgeFrameDecode(t *testing.T) {
	t.Parallel()
	raw := []byte(`{"type":"feedback.badge","badge":"fluency+1","phrase_block_id":"block-1","tier":"soft","session_id":"s1","turn_id":"turn-1","dedupe_key":"s1|turn-1|block-1"}`)
	var badge voiceproto.FeedbackBadge
	if err := json.Unmarshal(raw, &badge); err != nil {
		t.Fatalf("decode: %v", err)
	}
	if badge.Badge != "fluency+1" || badge.PhraseBlockID != "block-1" {
		t.Fatalf("unexpected decode: %+v", badge)
	}
	if typ, err := voiceproto.DecodeType(raw); err != nil || typ != voiceproto.TypeFeedbackBadge {
		t.Fatalf("DecodeType: got %q err %v", typ, err)
	}
}

func TestSpeechObservabilityEventSchemaMirrorPresent(t *testing.T) {
	t.Parallel()

	var doc map[string]any
	if err := json.Unmarshal(sharedschemas.SpeechObservabilityEventsV1, &doc); err != nil {
		t.Fatalf("schema json: %v", err)
	}
	if doc["title"] == nil {
		t.Fatal("schema missing title")
	}
	defs, ok := doc["$defs"].(map[string]any)
	if !ok {
		t.Fatal("schema missing $defs")
	}
	for _, name := range []string{
		"eventBase",
		"speechSessionStarted",
		"speechSessionFailed",
		"speechTurnEnded",
		"speechTransportDisconnected",
	} {
		if _, ok := defs[name]; !ok {
			t.Fatalf("schema missing $defs.%s", name)
		}
	}
}
