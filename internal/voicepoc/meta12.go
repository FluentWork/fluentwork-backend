package voicepoc

import (
	"os"
	"sort"
	"strings"
)

const (
	meta12QuotaEnv       = "VOLC_META12_QUOTA_OK"
	meta12NoTrainingEnv  = "VOLC_META12_NO_TRAINING_OK"
	meta12ConcurrencyEnv = "VOLC_META12_CONCURRENCY_OK"
)

// Meta12Status captures the B14 production-freeze prerequisites that live
// outside the duplex transport itself: quota, no-training confirmation, and
// concurrency confirmation.
type Meta12Status struct {
	QuotaOK       bool `json:"quota_ok"`
	NoTrainingOK  bool `json:"no_training_ok"`
	ConcurrencyOK bool `json:"concurrency_ok"`
	Closed        bool `json:"closed"`
}

// ResolveMeta12Status reads the local attestation env vars for the three meta
// prerequisites. These envs do not replace business approval; they make the
// current local freeze claim explicit in smoke outputs.
func ResolveMeta12Status() Meta12Status {
	status := Meta12Status{
		QuotaOK:       envBool(meta12QuotaEnv),
		NoTrainingOK:  envBool(meta12NoTrainingEnv),
		ConcurrencyOK: envBool(meta12ConcurrencyEnv),
	}
	status.Closed = status.QuotaOK && status.NoTrainingOK && status.ConcurrencyOK
	return status
}

func (s Meta12Status) Missing() []string {
	missing := make([]string, 0, 3)
	if !s.QuotaOK {
		missing = append(missing, "quota")
	}
	if !s.NoTrainingOK {
		missing = append(missing, "no_training")
	}
	if !s.ConcurrencyOK {
		missing = append(missing, "concurrency")
	}
	sort.Strings(missing)
	return missing
}

func (s Meta12Status) FreezeStatus() string {
	if s.Closed {
		return "meta12_closed"
	}
	return "engineering_candidate_only"
}

func envBool(name string) bool {
	switch strings.ToLower(strings.TrimSpace(os.Getenv(name))) {
	case "1", "true", "yes", "y", "on":
		return true
	default:
		return false
	}
}
