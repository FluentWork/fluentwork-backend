package session

import (
	"io"
	"log/slog"
	"testing"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/config"
)

func interruptedTestService(t *testing.T) *Service {
	t.Helper()
	return NewService(NewMemoryStore(), config.Config{}, slog.New(slog.NewTextHandler(io.Discard, nil)))
}

// The `interrupted` flag has to survive the app-server boundary.
//
// The gateway truncates an interrupted assistant reply to what had been
// delivered (77_ P1-14). If the flag is dropped here, the truncated text is
// indistinguishable from a reply that was simply that short — and the
// truncation becomes a silent edit of the record rather than a documented one.
func TestNormalizeEndUtterancesCarriesTheInterruptedFlag(t *testing.T) {
	t.Parallel()

	got, err := interruptedTestService(t).normalizeEndUtterances("s-1", []EndUtteranceItem{
		{Seq: 1, Speaker: SpeakerUser, Text: "we should ship it"},
		{Seq: 2, Speaker: SpeakerAI, Text: "Sounds good.", Interrupted: true},
		{Seq: 3, Speaker: SpeakerAI, Text: "Anything else?"},
	}, time.Now())
	if err != nil {
		t.Fatalf("normalizeEndUtterances: %v", err)
	}
	if len(got) != 3 {
		t.Fatalf("got %d utterances, want 3", len(got))
	}
	if !got[1].Interrupted {
		t.Fatalf("the interrupted flag was dropped at the boundary: %+v", got[1])
	}
	// It must not be set on everything — a flag that is always true is not a flag.
	if got[0].Interrupted || got[2].Interrupted {
		t.Fatalf("interrupted leaked onto turns that were not interrupted: %+v", got)
	}
}
