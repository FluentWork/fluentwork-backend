package voicepoc

import "testing"

func TestResolveMeta12Status(t *testing.T) {
	t.Setenv(meta12QuotaEnv, "true")
	t.Setenv(meta12NoTrainingEnv, "1")
	t.Setenv(meta12ConcurrencyEnv, "yes")

	got := ResolveMeta12Status()
	if !got.Closed || !got.QuotaOK || !got.NoTrainingOK || !got.ConcurrencyOK {
		t.Fatalf("ResolveMeta12Status() = %+v", got)
	}
	if len(got.Missing()) != 0 {
		t.Fatalf("expected no missing prereqs: %+v", got.Missing())
	}
	if got.FreezeStatus() != "meta12_closed" {
		t.Fatalf("FreezeStatus() = %q", got.FreezeStatus())
	}
}

func TestResolveMeta12StatusMissingItems(t *testing.T) {
	t.Setenv(meta12QuotaEnv, "")
	t.Setenv(meta12NoTrainingEnv, "false")
	t.Setenv(meta12ConcurrencyEnv, "1")

	got := ResolveMeta12Status()
	if got.Closed {
		t.Fatalf("ResolveMeta12Status() unexpectedly closed: %+v", got)
	}
	want := []string{"no_training", "quota"}
	missing := got.Missing()
	if len(missing) != len(want) {
		t.Fatalf("Missing() len = %d want %d (%v)", len(missing), len(want), missing)
	}
	for i := range want {
		if missing[i] != want[i] {
			t.Fatalf("Missing()[%d] = %q want %q", i, missing[i], want[i])
		}
	}
	if got.FreezeStatus() != "engineering_candidate_only" {
		t.Fatalf("FreezeStatus() = %q", got.FreezeStatus())
	}
}
