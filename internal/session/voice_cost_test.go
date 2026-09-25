package session

import (
	"context"
	"testing"
	"time"

	"github.com/FluentWork/fluentwork-backend/internal/aicost"
)

func fixedID() string { return "cost-1" }

func testSession() Session {
	return Session{ID: "s1", UserID: "u1"}
}

func TestBuildVoiceCostLogRecordsSecondsAndLeavesMoneyAtZero(t *testing.T) {
	t.Parallel()

	at := time.Date(2026, 9, 11, 3, 0, 0, 0, time.UTC)
	log := buildVoiceCostLog(testSession(), &VoiceUsageItem{
		UplinkMS:   12_000,
		DownlinkMS: 8_000,
		Model:      "1.2.6.0",
	}, fixedID, at)

	if log == nil {
		t.Fatal("usage was reported but no ledger row was built")
	}
	if log.TaskType != aicost.TaskTypeVoiceDuplex {
		t.Fatalf("task_type = %q, want %q", log.TaskType, aicost.TaskTypeVoiceDuplex)
	}
	if log.AudioSec != 20 {
		t.Fatalf("audio_sec = %d, want 20", log.AudioSec)
	}
	if log.Model != "1.2.6.0" {
		t.Fatalf("model = %q, want the measured one", log.Model)
	}
	// The whole point of this round: usage is a fact, money is not yet.
	if log.CostMicroYuan != 0 {
		t.Fatalf("cost_micro_yuan = %d; the vendor's rate is unverified, so a non-zero value would be a guess wearing a ledger row's authority", log.CostMicroYuan)
	}
	if log.UserID == nil || *log.UserID != "u1" {
		t.Fatalf("user_id = %v, want u1 so the row can be attributed", log.UserID)
	}
	if !log.CreatedAt.Equal(at) {
		t.Fatalf("created_at = %v, want %v", log.CreatedAt, at)
	}
}

// nil usage means the provider cannot measure (mock, dev-echo). That must leave
// **no row** — a row of zeroes is indistinguishable from a real session that
// moved no audio, and the difference decides whether anything ran at all.
func TestBuildVoiceCostLogIsNilWithoutUsage(t *testing.T) {
	t.Parallel()

	if log := buildVoiceCostLog(testSession(), nil, fixedID, time.Now()); log != nil {
		t.Fatalf("built %+v from absent usage, want nil", log)
	}
}

// A negative millisecond count would silently *reduce* the total. The wire says
// milliseconds, so clamp rather than trust it.
func TestBuildVoiceCostLogClampsNegativeMilliseconds(t *testing.T) {
	t.Parallel()

	log := buildVoiceCostLog(testSession(), &VoiceUsageItem{
		UplinkMS:   -5_000,
		DownlinkMS: 3_000,
	}, fixedID, time.Now())

	if log == nil {
		t.Fatal("expected a row")
	}
	if log.AudioSec != 3 {
		t.Fatalf("audio_sec = %d, want 3 (the negative side must not subtract)", log.AudioSec)
	}
}

// The row has to survive the trip through the *store*, not just the trip
// through buildVoiceCostLog. EndSession writes it into costLogs — a map
// NewMemoryStore forgot to initialise, so ending a session from a provider that
// reports usage (volc-duplex) panicked with "assignment to entry in nil map".
//
// The failure was near-invisible: the panic lands after the session is marked
// ended, so the gateway's retry short-circuits on Status == StatusEnded and the
// caller sees success. The row is simply never written — a silent hole in the
// cost ledger rather than a visible error.
func TestEndSessionRecordsVoiceCostLog(t *testing.T) {
	t.Parallel()

	store := NewMemoryStore()
	now := time.Date(2026, 9, 24, 7, 34, 0, 0, time.UTC)
	if err := store.CreateSession(context.Background(), Session{
		ID:        "s-voice",
		UserID:    "u-voice",
		Status:    StatusCreated,
		CreatedAt: now,
	}); err != nil {
		t.Fatalf("CreateSession: %v", err)
	}

	costLog := buildVoiceCostLog(
		Session{ID: "s-voice", UserID: "u-voice"},
		&VoiceUsageItem{UplinkMS: 4_000, DownlinkMS: 6_000, Model: "1.2.6.1"},
		func() string { return "cost-voice-1" },
		now,
	)
	if costLog == nil {
		t.Fatal("precondition: usage was reported, so a row must be built")
	}

	if _, _, _, err := store.EndSession(context.Background(), "s-voice", 10, nil, nil, now, costLog); err != nil {
		t.Fatalf("EndSession: %v", err)
	}

	stored, ok := store.costLogs[costLog.ID]
	if !ok {
		t.Fatal("EndSession dropped the voice cost log")
	}
	if stored.AudioSec != 10 {
		t.Fatalf("audio_sec = %d, want 10", stored.AudioSec)
	}
	if stored.TaskType != aicost.TaskTypeVoiceDuplex {
		t.Fatalf("task_type = %q, want %q", stored.TaskType, aicost.TaskTypeVoiceDuplex)
	}
}

// Floor, not round: a partial second is not a second of audio, and rounding up
// inflates every session in the same direction.
func TestBuildVoiceCostLogFloorsPartialSeconds(t *testing.T) {
	t.Parallel()

	log := buildVoiceCostLog(testSession(), &VoiceUsageItem{
		UplinkMS:   999,
		DownlinkMS: 0,
	}, fixedID, time.Now())

	if log == nil {
		t.Fatal("expected a row")
	}
	if log.AudioSec != 0 {
		t.Fatalf("audio_sec = %d, want 0", log.AudioSec)
	}
}
