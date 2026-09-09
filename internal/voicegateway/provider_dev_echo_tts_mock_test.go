package voicegateway_test

import (
	"context"
	"encoding/binary"
	"encoding/json"
	"testing"

	"github.com/FluentWork/fluentwork-backend/internal/voicegateway"
	"github.com/FluentWork/fluentwork-backend/internal/voiceproto"
)

func TestDevEchoTTSMock_EmitsStartTenBinaryEnd(t *testing.T) {
	t.Parallel()

	provider := voicegateway.NewDevEchoVoiceProvider("echo-me", nil)
	provider.TTSMock = true
	provider.Fixture = voicegateway.DevEchoFixtureGenerator(20)

	sess, err := provider.Open(context.Background(), voicegateway.ConsumedTicket{
		SessionID: "s-tts",
		UserID:    "u-tts",
	})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close(context.Background()) })

	outbound, err := sess.HandleClientControl(
		context.Background(),
		voiceproto.TypeUserSpeechEnd,
		[]byte(`{"type":"user.speech.end","turn_id":"turn-9"}`),
	)
	if err != nil {
		t.Fatalf("HandleClientControl: %v", err)
	}

	// ASR + start + 10 binary + end + turn.end
	if len(outbound) != 14 {
		t.Fatalf("got %d outbound items, want 14", len(outbound))
	}

	assertControlType(t, outbound[0], voiceproto.TypeClientASRTranscription)
	if outbound[0].ServerASRText != "echo-me" {
		t.Fatalf("ServerASRText = %q", outbound[0].ServerASRText)
	}

	start := asMap(t, outbound[1].Control)
	if start["type"] != "ai.tts.start" {
		t.Fatalf("expected ai.tts.start, got %#v", start)
	}
	if start["turn_id"] != "turn-9" {
		t.Fatalf("turn_id = %v", start["turn_id"])
	}
	if start["voice_id"] != "mock_voice_01" {
		t.Fatalf("voice_id = %v", start["voice_id"])
	}
	if start["codec"] != "opus" {
		t.Fatalf("codec = %v", start["codec"])
	}
	if asInt(t, start["sample_rate"]) != 24000 {
		t.Fatalf("sample_rate = %v", start["sample_rate"])
	}

	for i := 0; i < 10; i++ {
		item := outbound[2+i]
		if len(item.Binary) < 5 {
			t.Fatalf("audio[%d] too short: %d", i, len(item.Binary))
		}
		seq := binary.BigEndian.Uint32(item.Binary[:4])
		if seq != uint32(i) {
			t.Fatalf("audio[%d] seq = %d", i, seq)
		}
		wantPayload := []byte("mock-opus-frame-" + itoa(i))
		gotPayload := item.Binary[4:]
		if string(gotPayload) != string(wantPayload) {
			t.Fatalf("audio[%d] payload = %q, want %q", i, gotPayload, wantPayload)
		}
	}

	end := asMap(t, outbound[12].Control)
	if end["type"] != "ai.tts.end" {
		t.Fatalf("expected ai.tts.end, got %#v", end)
	}
	if end["completion_status"] != "ok" {
		t.Fatalf("completion_status = %v", end["completion_status"])
	}
	if asInt(t, end["duration_ms"]) != 200 {
		t.Fatalf("duration_ms = %v", end["duration_ms"])
	}

	turnEnd := asMap(t, outbound[13].Control)
	if turnEnd["type"] != voiceproto.TypeAITurnEnd {
		t.Fatalf("expected ai.turn.end, got %#v", turnEnd)
	}
}

func TestDevEchoTTSMock_SecondTurnContinuesSeq(t *testing.T) {
	t.Parallel()

	provider := voicegateway.NewDevEchoVoiceProvider("", nil)
	provider.TTSMock = true
	sess, err := provider.Open(context.Background(), voicegateway.ConsumedTicket{SessionID: "s"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close(context.Background()) })

	first, err := sess.HandleClientControl(context.Background(), voiceproto.TypeUserSpeechEnd, []byte(`{"turn_id":"t1"}`))
	if err != nil {
		t.Fatalf("first turn: %v", err)
	}
	second, err := sess.HandleClientControl(context.Background(), voiceproto.TypeUserSpeechEnd, []byte(`{"turn_id":"t2"}`))
	if err != nil {
		t.Fatalf("second turn: %v", err)
	}

	lastFirst := first[10].Binary // start at [0], audio 1-10 → index 10 is last audio
	firstSecond := second[1].Binary
	if binary.BigEndian.Uint32(lastFirst[:4]) != 9 {
		t.Fatalf("first turn last seq = %d", binary.BigEndian.Uint32(lastFirst[:4]))
	}
	if binary.BigEndian.Uint32(firstSecond[:4]) != 10 {
		t.Fatalf("second turn first seq = %d, want 10", binary.BigEndian.Uint32(firstSecond[:4]))
	}
}

func assertControlType(t *testing.T, item voicegateway.ProviderOutbound, want string) {
	t.Helper()
	got := asMap(t, item.Control)
	if got["type"] != want {
		t.Fatalf("type = %v, want %s", got["type"], want)
	}
}

func asMap(t *testing.T, v any) map[string]any {
	t.Helper()
	if m, ok := v.(map[string]any); ok {
		return m
	}
	raw, err := json.Marshal(v)
	if err != nil {
		t.Fatalf("marshal control: %v", err)
	}
	var out map[string]any
	if err := json.Unmarshal(raw, &out); err != nil {
		t.Fatalf("decode control: %v", err)
	}
	return out
}

func asInt(t *testing.T, v any) int {
	t.Helper()
	switch n := v.(type) {
	case int:
		return n
	case int64:
		return int(n)
	case float64:
		return int(n)
	default:
		t.Fatalf("not a number: %T %#v", v, v)
		return 0
	}
}

func itoa(n int) string {
	return []string{"0", "1", "2", "3", "4", "5", "6", "7", "8", "9"}[n]
}
