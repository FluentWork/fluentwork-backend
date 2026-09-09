package tts

import (
	"context"
	"encoding/base64"
	"encoding/json"
	"errors"
	"io"
	"net/http"
	"net/http/httptest"
	"sync/atomic"
	"testing"
	"time"
)

func TestVolcStreaming_Stream_Success(t *testing.T) {
	fixed := time.Date(2026, 9, 9, 21, 0, 0, 0, time.UTC)
	frame0 := base64.StdEncoding.EncodeToString([]byte("opus-0"))
	frame1 := base64.StdEncoding.EncodeToString([]byte("opus-1"))

	var gotKey, gotResource, gotRequestID string
	var body volcTTSRequest
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("X-Api-Key")
		gotResource = r.Header.Get("X-Api-Resource-Id")
		gotRequestID = r.Header.Get("X-Api-Request-Id")
		if r.Header.Get("Content-Type") != "application/json" {
			t.Errorf("Content-Type = %q", r.Header.Get("Content-Type"))
		}
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("decode body: %v", err)
			w.WriteHeader(http.StatusBadRequest)
			return
		}
		w.Header().Set("X-Tt-Logid", "log-success")
		w.WriteHeader(http.StatusOK)
		_, _ = w.Write(concatVolcFrames(
			volcTTSFrame{Code: 0, Data: frame0},
			volcTTSFrame{Code: 0, Data: ""},
			volcTTSFrame{Code: 0, Data: frame1},
			volcTTSFrame{Code: volcTTSSuccessCode, Message: "ok"},
		))
	}))
	t.Cleanup(srv.Close)

	provider := &VolcStreamingProvider{
		APIKey:       "test-key",
		Endpoint:     srv.URL,
		ResourceID:   "seed-tts-2.0",
		HTTPClient:   srv.Client(),
		now:          func() time.Time { return fixed },
		newRequestID: func() string { return "req-1" },
	}

	ch, err := provider.Stream(context.Background(), "  hello fluentwork  ", VoiceConfig{
		VoiceID: "zh_male_tech_01",
		Speed:   0.9,
	})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	var got []AudioChunk
	for chunk := range ch {
		got = append(got, chunk)
	}
	if len(got) != 2 {
		t.Fatalf("got %d chunks, want 2: %+v", len(got), got)
	}
	if string(got[0].Data) != "opus-0" || got[0].Seq != 0 || got[0].IsFinal || got[0].DetectedAt != fixed.UnixMilli() {
		t.Fatalf("chunk0 = %+v", got[0])
	}
	if string(got[1].Data) != "opus-1" || got[1].Seq != 1 || !got[1].IsFinal {
		t.Fatalf("chunk1 = %+v", got[1])
	}

	if gotKey != "test-key" || gotResource != "seed-tts-2.0" || gotRequestID != "req-1" {
		t.Fatalf("headers key=%q resource=%q request=%q", gotKey, gotResource, gotRequestID)
	}
	if body.ReqParams.Text != "hello fluentwork" || body.ReqParams.Speaker != "zh_male_tech_01" {
		t.Fatalf("body text/speaker = %+v", body.ReqParams)
	}
	if body.ReqParams.AudioParams.Format != defaultVolcTTSFormat || body.ReqParams.AudioParams.SampleRate != defaultVolcTTSSampleRate {
		t.Fatalf("audio params = %+v", body.ReqParams.AudioParams)
	}
	if body.ReqParams.AudioParams.SpeechRate != -10 {
		t.Fatalf("speech_rate = %d, want -10", body.ReqParams.AudioParams.SpeechRate)
	}
}

func TestVolcStreaming_Stream_ContextCancel(t *testing.T) {
	started := make(chan struct{})
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		close(started)
		w.WriteHeader(http.StatusOK)
		if f, ok := w.(http.Flusher); ok {
			f.Flush()
		}
		<-r.Context().Done()
	}))
	t.Cleanup(srv.Close)

	provider := &VolcStreamingProvider{
		APIKey:     "test-key",
		Endpoint:   srv.URL,
		HTTPClient: srv.Client(),
	}

	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	ch, err := provider.Stream(ctx, "hello", VoiceConfig{VoiceID: "zh_female_vv_jupiter_bigtts"})
	if err != nil {
		t.Fatalf("Stream: %v", err)
	}

	select {
	case <-started:
	case <-time.After(2 * time.Second):
		t.Fatal("server did not see the request")
	}
	cancel()

	select {
	case chunk, ok := <-ch:
		if ok {
			t.Fatalf("unexpected chunk %#v", chunk)
		}
	case <-time.After(2 * time.Second):
		t.Fatal("channel did not close after cancel")
	}
}

func TestVolcStreaming_Stream_5xxRetry(t *testing.T) {
	t.Run("retries once then succeeds", func(t *testing.T) {
		var hits atomic.Int32
		frame := base64.StdEncoding.EncodeToString([]byte("opus-ok"))
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			n := hits.Add(1)
			if n == 1 {
				w.WriteHeader(http.StatusServiceUnavailable)
				_, _ = io.WriteString(w, "busy")
				return
			}
			w.WriteHeader(http.StatusOK)
			_, _ = w.Write(concatVolcFrames(
				volcTTSFrame{Code: 0, Data: frame},
				volcTTSFrame{Code: volcTTSSuccessCode, Message: "ok"},
			))
		}))
		t.Cleanup(srv.Close)

		provider := &VolcStreamingProvider{
			APIKey:     "test-key",
			Endpoint:   srv.URL,
			HTTPClient: srv.Client(),
		}
		ch, err := provider.Stream(context.Background(), "hello", VoiceConfig{VoiceID: "v1"})
		if err != nil {
			t.Fatalf("Stream: %v", err)
		}
		var got []AudioChunk
		for chunk := range ch {
			got = append(got, chunk)
		}
		if hits.Load() != 2 {
			t.Fatalf("hits = %d, want 2", hits.Load())
		}
		if len(got) != 1 || string(got[0].Data) != "opus-ok" || !got[0].IsFinal {
			t.Fatalf("chunks = %+v", got)
		}
	})

	t.Run("gives up after one retry", func(t *testing.T) {
		var hits atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hits.Add(1)
			w.Header().Set("X-Tt-Logid", "log-5xx")
			w.WriteHeader(http.StatusBadGateway)
		}))
		t.Cleanup(srv.Close)

		provider := &VolcStreamingProvider{
			APIKey:     "test-key",
			Endpoint:   srv.URL,
			HTTPClient: srv.Client(),
		}
		_, err := provider.Stream(context.Background(), "hello", VoiceConfig{VoiceID: "v1"})
		if !errors.Is(err, ErrHTTPStatus) {
			t.Fatalf("err = %v, want ErrHTTPStatus", err)
		}
		if hits.Load() != 2 {
			t.Fatalf("hits = %d, want 2", hits.Load())
		}
	})

	t.Run("does not retry 4xx", func(t *testing.T) {
		var hits atomic.Int32
		srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, _ *http.Request) {
			hits.Add(1)
			w.WriteHeader(http.StatusBadRequest)
		}))
		t.Cleanup(srv.Close)

		provider := &VolcStreamingProvider{
			APIKey:     "test-key",
			Endpoint:   srv.URL,
			HTTPClient: srv.Client(),
		}
		_, err := provider.Stream(context.Background(), "hello", VoiceConfig{VoiceID: "v1"})
		if !errors.Is(err, ErrHTTPStatus) {
			t.Fatalf("err = %v, want ErrHTTPStatus", err)
		}
		if hits.Load() != 1 {
			t.Fatalf("hits = %d, want 1", hits.Load())
		}
	})
}

func concatVolcFrames(frames ...volcTTSFrame) []byte {
	raw, err := json.Marshal(frames)
	if err != nil {
		panic(err)
	}
	// Marshal of a slice wraps with []; Volc sends concatenated objects.
	var unpacked []json.RawMessage
	if err := json.Unmarshal(raw, &unpacked); err != nil {
		panic(err)
	}
	out := make([]byte, 0, len(raw))
	for _, item := range unpacked {
		out = append(out, item...)
	}
	return out
}
