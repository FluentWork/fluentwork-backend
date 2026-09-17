package corpus

import "time"

// ApplyJudge advances the fixed-ladder schedule after one attempt (PRD §5.3.2):
// 24h / 7d / 30d on success, 1h on failure, promotion at three in a row.
//
// It lives in corpus rather than drill because two writers share the ladder:
// drill judging and the B7 hit writeback, the latter running inside the corpus
// store transaction. drill imports corpus, so corpus cannot import drill;
// drill.ApplyJudge delegates here to keep its API.
func ApplyJudge(block PhraseBlock, pass bool, now time.Time) PhraseBlock {
	out := block
	now = now.UTC()
	out.UpdatedAt = now
	if !pass {
		out.SuccessStreak = 0
		out.State = StateTraining
		out.NextDueAt = now.Add(time.Hour)
		return out
	}
	if block.State == StateAutomated {
		out.NextDueAt = now.Add(30 * 24 * time.Hour)
		return out
	}
	out.SuccessStreak = block.SuccessStreak + 1
	if out.SuccessStreak >= 3 {
		out.State = StateAutomated
		out.NextDueAt = now.Add(7 * 24 * time.Hour)
		return out
	}
	out.State = StateTraining
	out.NextDueAt = now.Add(24 * time.Hour)
	return out
}
