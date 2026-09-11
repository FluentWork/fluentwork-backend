package voicepoc

import (
	"encoding/json"
	"os"
	"testing"
)

// The recorded 30-sample measurement, re-scored offline.
//
// This is the guard on the number the tool prints. It reads the transcripts
// captured in `docs/52`'s report — the actual ASR output, not a fixture — so it
// re-derives the headline figures **mechanically** instead of asserting numbers
// someone computed by hand.
//
// That distinction is the whole point. `docs/52` recorded 14.7% raw and 10.4%
// normalised. The raw figure reproduces exactly; **the normalised one does not**
// — applying the rules that document names ("数字读法 + 缩写展开") exhaustively
// gives 7.9%, and adding the compound rule gives 6.4%. Neither is 10.4%, which
// sits between two rule sets and matches neither. It appears to have counted
// the specific instances someone noticed rather than the ones the rules imply.
//
// So this test pins the reproducible figures and names what they replace. A
// change to the normaliser moves them, and moving them must be deliberate —
// which is exactly what was missing when 10.4% was written down.
//
// Reads from `docs/` rather than a `testdata/` copy on purpose: the report is
// the measurement record, and a second copy of it would be a second thing to
// keep in sync. If the artifact moves, this test says so rather than quietly
// skipping — a guard that can skip itself is not a guard.
func TestRecordedSampleRunRescoresToTheDocumentedBaseline(t *testing.T) {
	t.Parallel()

	const reportPath = "../../docs/52_wer_report_2026-09-11.json"
	raw, err := os.ReadFile(reportPath)
	if err != nil {
		t.Fatalf("the WER measurement record is missing (%v); if it moved, point this test at it — "+
			"skipping would retire the only guard on the published number", err)
	}

	var doc struct {
		Samples []struct {
			ID         string `json:"id"`
			Reference  string `json:"reference"`
			Hypothesis string `json:"hypothesis"`
		} `json:"samples"`
	}
	if err := json.Unmarshal(raw, &doc); err != nil {
		t.Fatalf("parse %s: %v", reportPath, err)
	}
	if len(doc.Samples) != 30 {
		t.Fatalf("expected the 30-sample set, got %d", len(doc.Samples))
	}

	var rawTot, normTot WERResult
	for _, s := range doc.Samples {
		r := ScoreWER(s.Reference, s.Hypothesis)
		n := ScoreWERNormalized(s.Reference, s.Hypothesis)

		rawTot.Substitutions += r.Substitutions
		rawTot.Deletions += r.Deletions
		rawTot.Insertions += r.Insertions
		rawTot.ReferenceLength += r.ReferenceLength

		normTot.Substitutions += n.Substitutions
		normTot.Deletions += n.Deletions
		normTot.Insertions += n.Insertions
		normTot.ReferenceLength += n.ReferenceLength
	}

	// Raw — unchanged by the normaliser, and the figure `docs/52` records.
	assertWER(t, "raw", rawTot, 39, 266, 14.7)
	// Normalised — mechanically derived. Supersedes the 10.4% in docs/52 §2,
	// which is not reproducible from the rules that document states.
	assertWER(t, "normalised", normTot, 18, 282, 6.4)

	// The gap is the measurement: how much of the raw error was formatting
	// rather than mishearing. It is the number worth watching over time.
	if gap := rawTot.Errors() - normTot.Errors(); gap != 21 {
		t.Fatalf("formatting-only errors = %d, want 21; the two rates move together and the "+
			"gap is what says whether the recogniser is the problem", gap)
	}
}

func assertWER(t *testing.T, label string, got WERResult, errors, refLen int, rate float64) {
	t.Helper()
	if got.Errors() != errors || got.ReferenceLength != refLen {
		t.Fatalf("%s: S=%d D=%d I=%d N=%d (errors=%d), want errors=%d N=%d",
			label, got.Substitutions, got.Deletions, got.Insertions,
			got.ReferenceLength, got.Errors(), errors, refLen)
	}
	if r := got.Rate() * 100; r < rate-0.05 || r > rate+0.05 {
		t.Fatalf("%s rate = %.1f%%, want %.1f%%", label, r, rate)
	}
}
