package voicegateway

import (
	"testing"

	"github.com/FluentWork/fluentwork-backend/internal/voiceproto"
)

// TestExtractServerASRText covers the pure helper in isolation: it scans the
// produced ProviderOutbounds in order and returns the first non-empty
// ServerASRText. The handler relies on this so a single ASR text returned
// alongside several control frames is picked up exactly once.
func TestExtractServerASRText(t *testing.T) {
	t.Parallel()

	cases := []struct {
		name string
		in   []ProviderOutbound
		want string
	}{
		{
			name: "empty slice",
			in:   nil,
			want: "",
		},
		{
			name: "single empty",
			in:   []ProviderOutbound{{Control: "x"}},
			want: "",
		},
		{
			name: "first match wins",
			in: []ProviderOutbound{
				{ServerASRText: "first"},
				{ServerASRText: "second"},
			},
			want: "first",
		},
		{
			name: "skips empty entries",
			in: []ProviderOutbound{
				{Control: "noise"},
				{ServerASRText: ""},
				{ServerASRText: "real"},
			},
			want: "real",
		},
		{
			name: "whitespace is still returned (handler does its own trim)",
			in: []ProviderOutbound{
				{ServerASRText: "   "},
			},
			want: "   ",
		},
		{
			name: "control frames alongside",
			in: []ProviderOutbound{
				{Control: voiceproto.ClientASRTranscription{Type: voiceproto.TypeClientASRTranscription, Text: "hi"}},
				{ServerASRText: "real"},
				{Control: voiceproto.AITurnEnd{Type: voiceproto.TypeAITurnEnd}},
			},
			want: "real",
		},
	}

	for _, tc := range cases {
		tc := tc
		t.Run(tc.name, func(t *testing.T) {
			t.Parallel()
			got := extractServerASRText(tc.in)
			if got != tc.want {
				t.Fatalf("want %q, got %q", tc.want, got)
			}
		})
	}
}
