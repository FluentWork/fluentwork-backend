package voicegateway

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"testing"
)

// captureEndBody runs one End call against a throwaway server and returns the
// decoded body. The wire shape is what app-server reads, so it is what the test
// has to look at — asserting on EndSessionRequest would prove nothing about
// what went out.
func captureEndBody(t *testing.T, req EndSessionRequest) map[string]any {
	t.Helper()

	captured := make(chan map[string]any, 1)
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		raw, _ := io.ReadAll(r.Body)
		var body map[string]any
		_ = json.Unmarshal(raw, &body)
		captured <- body
		w.Header().Set("Content-Type", "application/json")
		_, _ = w.Write([]byte(`{"session_id":"s1"}`))
	}))
	t.Cleanup(srv.Close)

	client := HTTPSessionClient{BaseURL: srv.URL, Token: "test", HTTPClient: srv.Client()}
	if err := client.End(context.Background(), req); err != nil {
		t.Fatalf("End: %v", err)
	}
	return <-captured
}

// The usage the gateway measured has to survive to the app-server, otherwise
// the ledger row it would produce has nothing to record.
func TestEndSessionCarriesVoiceUsageWhenMeasured(t *testing.T) {
	t.Parallel()

	body := captureEndBody(t, EndSessionRequest{
		SessionID:   "s1",
		DurationSec: 42,
		Reason:      "user",
		VoiceUsage:  &VoiceUsage{UplinkMS: 12_345, DownlinkMS: 6_789},
	})

	usage, ok := body["voice_usage"].(map[string]any)
	if !ok {
		t.Fatalf("voice_usage missing from the wire body: %#v", body)
	}
	if usage["uplink_ms"] != float64(12_345) {
		t.Fatalf("uplink_ms = %v, want 12345", usage["uplink_ms"])
	}
	if usage["downlink_ms"] != float64(6_789) {
		t.Fatalf("downlink_ms = %v, want 6789", usage["downlink_ms"])
	}
}

// `omitempty` is load-bearing, not tidiness: a session whose provider cannot
// report usage (mock, dev-echo) must send no field at all, so the app-server
// writes no cost row. A zero-valued object would look like a measured session
// that happened to move no audio.
func TestEndSessionOmitsVoiceUsageWhenUnmeasured(t *testing.T) {
	t.Parallel()

	body := captureEndBody(t, EndSessionRequest{
		SessionID:   "s1",
		DurationSec: 42,
		Reason:      "user",
	})

	if _, present := body["voice_usage"]; present {
		t.Fatalf("voice_usage present on a session that measured nothing: %#v", body)
	}
}

// The snapshot is what the handler hands to End, and it has to stay nil for a
// provider that does not implement the reporter — the same distinction the
// wire test above depends on.
func TestSnapshotVoiceUsageIsNilWithoutAReporter(t *testing.T) {
	t.Parallel()

	if got := (&sessionRuntime{}).snapshotVoiceUsage(); got != nil {
		t.Fatalf("runtime with no provider reported %+v, want nil", got)
	}

	sess, err := MockVoiceProvider{}.Open(context.Background(), ConsumedTicket{})
	if err != nil {
		t.Fatalf("open mock session: %v", err)
	}
	rt := &sessionRuntime{provider: sess}
	if got := rt.snapshotVoiceUsage(); got != nil {
		t.Fatalf("mock provider reported %+v, want nil", got)
	}
}
