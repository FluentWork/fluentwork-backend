package corpus

import "time"

// Schedule is the MVP fixed ladder of PRD §5.3.2, with the spacing the server
// has been configured to use (E3: 调度参数服务端可配置).
//
// One value serves both writers — drill judging and the B7 hit writeback — so a
// real-world hit can never promote a block on different terms than a flash-drill
// answer. Wiring builds it once from config and hands the same struct to both;
// see cmd/app-server.
type Schedule struct {
	// PromoteStreak is how many consecutive successes turn a block green
	// (默认 3).
	PromoteStreak int
	// TrainingInterval is the next-due delay while a block is still 黄
	// (默认 24h).
	TrainingInterval time.Duration
	// AutomatedInterval is the delay applied on the success that promotes the
	// block to 绿 (默认 7d).
	AutomatedInterval time.Duration
	// AutomatedReviewInterval is the longer delay for re-verifying an already
	// green block (默认 30d) — 低接触，不是免复习.
	AutomatedReviewInterval time.Duration
	// FailInterval is how soon a failed block comes back (默认 1h).
	FailInterval time.Duration
}

// DefaultSchedule is the PRD default ladder: 3 successes, 24h / 7d / 30d, 1h.
func DefaultSchedule() Schedule {
	return Schedule{
		PromoteStreak:           3,
		TrainingInterval:        24 * time.Hour,
		AutomatedInterval:       7 * 24 * time.Hour,
		AutomatedReviewInterval: 30 * 24 * time.Hour,
		FailInterval:            time.Hour,
	}
}

// Normalize replaces zero fields with their defaults, so a partially filled
// Schedule (or a zero value) behaves exactly like the PRD ladder rather than
// scheduling everything for "now".
func (s Schedule) Normalize() Schedule {
	out := s
	defaults := DefaultSchedule()
	if out.PromoteStreak < 1 {
		out.PromoteStreak = defaults.PromoteStreak
	}
	if out.TrainingInterval <= 0 {
		out.TrainingInterval = defaults.TrainingInterval
	}
	if out.AutomatedInterval <= 0 {
		out.AutomatedInterval = defaults.AutomatedInterval
	}
	if out.AutomatedReviewInterval <= 0 {
		out.AutomatedReviewInterval = defaults.AutomatedReviewInterval
	}
	if out.FailInterval <= 0 {
		out.FailInterval = defaults.FailInterval
	}
	return out
}

// ApplyJudge advances the ladder after one attempt (PRD §5.3.2).
func (s Schedule) ApplyJudge(block PhraseBlock, pass bool, now time.Time) PhraseBlock {
	s = s.Normalize()
	out := block
	now = now.UTC()
	out.UpdatedAt = now
	if !pass {
		out.SuccessStreak = 0
		out.State = StateTraining
		out.NextDueAt = now.Add(s.FailInterval)
		return out
	}
	if block.State == StateAutomated {
		out.NextDueAt = now.Add(s.AutomatedReviewInterval)
		return out
	}
	out.SuccessStreak = block.SuccessStreak + 1
	if out.SuccessStreak >= s.PromoteStreak {
		out.State = StateAutomated
		out.NextDueAt = now.Add(s.AutomatedInterval)
		return out
	}
	out.State = StateTraining
	out.NextDueAt = now.Add(s.TrainingInterval)
	return out
}

// ApplyJudge advances the ladder with the default schedule.
//
// It lives in corpus rather than drill because two writers share the ladder:
// drill judging and the B7 hit writeback, the latter running inside the corpus
// store transaction. drill imports corpus, so corpus cannot import drill;
// drill.ApplyJudge delegates here to keep its API.
func ApplyJudge(block PhraseBlock, pass bool, now time.Time) PhraseBlock {
	return DefaultSchedule().ApplyJudge(block, pass, now)
}
