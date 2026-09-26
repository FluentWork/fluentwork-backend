package voicegateway

import (
	"context"
	"testing"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/voiceproto"
)

func TestHandler_SessionEndLogsTheTurnRejectionCounts(t *testing.T) {
	t.Parallel()

	rig := newRescueRig(t, rescueAITurn{ttsEnd: true, turnEnd: true}, nil)
	conn, _ := rig.connect(t)

	readCtx, cancel := context.WithTimeout(context.Background(), rescueTestWait)
	defer cancel()

	rescueSendFrame(readCtx, t, conn, voiceproto.ClientTurnAbort{
		Type:    voiceproto.TypeClientTurnAbort,
		Outcome: voiceproto.ClientTurnAbortUserAbandoned,
	})
	rescueSendFrame(readCtx, t, conn, voiceproto.SessionEnd{Type: voiceproto.TypeSessionEnd, Reason: "user"})

	rescueWaitForType(readCtx, t, conn, voiceproto.TypeSessionEnd)

	value, ok := rig.logs.infoAttr("session.end persisted", "turn_rejected")
	if !ok {
		t.Fatal("session.end persisted carried no turn_rejected field")
	}
	counts, ok := value.Any().(map[string]int)
	if !ok {
		t.Fatalf("turn_rejected = %v (%T), want a map of event names to counts", value.Any(), value.Any())
	}
	if got := counts["client.turn.abort"]; got != 1 {
		t.Fatalf("turn_rejected = %v, want client.turn.abort counted once", counts)
	}
}

func TestHandler_SessionExitLogsTheTurnRejectionCounts(t *testing.T) {
	t.Parallel()

	rig := newRescueRig(t, rescueAITurn{ttsEnd: true, turnEnd: true}, nil)
	conn, _ := rig.connect(t)

	writeCtx, cancelWrite := context.WithTimeout(context.Background(), rescueTestWait)
	defer cancelWrite()

	rescueSendFrame(writeCtx, t, conn, voiceproto.ClientTurnAbort{
		Type:    voiceproto.TypeClientTurnAbort,
		Outcome: voiceproto.ClientTurnAbortUserAbandoned,
	})
	rescueSendFrame(writeCtx, t, conn, voiceproto.Ping{Type: voiceproto.TypePing})
	rescueWaitForType(writeCtx, t, conn, voiceproto.TypePong)

	_ = conn.CloseNow()

	deadline := time.Now().Add(rescueTestWait)
	for {
		if value, ok := rig.logs.infoAttr("session.exit persisted", "turn_rejected"); ok {
			counts, isMap := value.Any().(map[string]int)
			if !isMap {
				t.Fatalf("turn_rejected = %v (%T), want a map of event names to counts", value.Any(), value.Any())
			}
			if got := counts["client.turn.abort"]; got != 1 {
				t.Fatalf("turn_rejected = %v, want client.turn.abort counted once", counts)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatal("session.exit persisted carried no turn_rejected field")
		}
		time.Sleep(5 * time.Millisecond)
	}
}
