package voicegateway_test

import (
	"bytes"
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

	// ASR + start + 250 binary + end + turn.end
	const audioFrames = 250
	if len(outbound) != audioFrames+4 {
		t.Fatalf("got %d outbound items, want %d", len(outbound), audioFrames+4)
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
	// The declared codec has to describe the bytes that follow. It used to say
	// "opus" over ASCII placeholder text, which no client could ever play: the
	// mock proved frames arrived, never that audio came out.
	if start["codec"] != "pcm" {
		t.Fatalf("codec = %v", start["codec"])
	}
	if asInt(t, start["sample_rate"]) != 16000 {
		t.Fatalf("sample_rate = %v", start["sample_rate"])
	}

	tone := voicegateway.DevEchoFixtureGenerator(audioFrames * 20)
	for i := 0; i < audioFrames; i++ {
		item := outbound[2+i]
		if len(item.Binary) < 5 {
			t.Fatalf("audio[%d] too short: %d", i, len(item.Binary))
		}
		seq := binary.BigEndian.Uint32(item.Binary[:4])
		if seq != uint32(i) {
			t.Fatalf("audio[%d] seq = %d", i, seq)
		}
		gotPayload := item.Binary[4:]
		if len(gotPayload)%2 != 0 {
			t.Fatalf("audio[%d] payload has an odd byte count: %d — not PCM16", i, len(gotPayload))
		}
		wantPayload := tone[i*640 : (i+1)*640]
		if !bytes.Equal(gotPayload, wantPayload) {
			t.Fatalf("audio[%d] payload is not the 1kHz tone slice", i)
		}
	}

	end := asMap(t, outbound[2+audioFrames].Control)
	if end["type"] != "ai.tts.end" {
		t.Fatalf("expected ai.tts.end, got %#v", end)
	}
	if end["completion_status"] != "ok" {
		t.Fatalf("completion_status = %v", end["completion_status"])
	}
	if asInt(t, end["duration_ms"]) != 5000 {
		t.Fatalf("duration_ms = %v", end["duration_ms"])
	}

	turnEnd := asMap(t, outbound[3+audioFrames].Control)
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

	// 空 echoText：outbound = [start, 250 audio, end, turn.end] → 最后一帧音频在 [250]。
	const frames = 250
	lastFirst := first[frames].Binary
	firstSecond := second[1].Binary
	if got := binary.BigEndian.Uint32(lastFirst[:4]); got != frames-1 {
		t.Fatalf("first turn last seq = %d, want %d", got, frames-1)
	}
	if got := binary.BigEndian.Uint32(firstSecond[:4]); got != frames {
		t.Fatalf("second turn first seq = %d, want %d", got, frames)
	}
}

func TestDevEchoTTSMock_FramesArePCM16WithEvenLength(t *testing.T) {
	t.Parallel()

	provider := voicegateway.NewDevEchoVoiceProvider("", nil)
	provider.TTSMock = true
	sess, err := provider.Open(context.Background(), voicegateway.ConsumedTicket{SessionID: "s-pcm"})
	if err != nil {
		t.Fatalf("Open: %v", err)
	}
	t.Cleanup(func() { _ = sess.Close(context.Background()) })

	outbound, err := sess.HandleClientControl(
		context.Background(), voiceproto.TypeUserSpeechEnd, []byte(`{"turn_id":"t-pcm"}`),
	)
	if err != nil {
		t.Fatalf("HandleClientControl: %v", err)
	}

	for i, item := range outbound {
		if item.Binary == nil {
			continue
		}
		payload := item.Binary[4:]
		if len(payload) == 0 || len(payload)%2 != 0 {
			t.Fatalf("frame %d payload is not PCM16-aligned: %d bytes", i, len(payload))
		}
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
