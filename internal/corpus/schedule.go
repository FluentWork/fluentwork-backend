package corpus

import "time"

func ApplySuccess(block PhraseBlock, now time.Time) PhraseBlock {
	out := cloneBlock(block)
	out.SuccessStreak++
	switch {
	case out.SuccessStreak >= 3:
		out.State = StateAutomated
		out.NextDueAt = nextDueDays(now, 7)
	case out.SuccessStreak == 2:
		out.State = StateTraining
		out.NextDueAt = nextDueDays(now, 3)
	default:
		out.State = StateTraining
		out.NextDueAt = nextDueDays(now, 1)
	}
	out.UpdatedAt = now.UTC()
	return out
}

func ApplyFailure(block PhraseBlock, now time.Time) PhraseBlock {
	out := cloneBlock(block)
	out.SuccessStreak = 0
	out.State = StateTraining
	out.NextDueAt = nextDueDays(now, 1)
	out.UpdatedAt = now.UTC()
	return out
}

func nextDueDays(now time.Time, days int) time.Time {
	base := now.UTC().Truncate(24 * time.Hour)
	return base.Add(time.Duration(days) * 24 * time.Hour)
}
