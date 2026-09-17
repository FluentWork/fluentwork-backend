-- V2.x E2 appeal (PRD §7.5 V1.7 定案).
--
-- A drill answer is a few seconds of speech, so its ASR error rate is far above
-- a long sentence's — and "you said it right but were marked wrong" is exactly
-- the failure that kills active-recall training. The appeal therefore does two
-- things at once, and they are deliberately separate:
--
--   * the round's settlement stands: semantic_pass keeps what the judge said;
--   * the block is not charged with the failure: prev_* snapshots the schedule
--     as it stood before the attempt, so an appeal can put it back exactly, and
--     next_due_at returns to its original time ("未做判定", PRD §7.5).
--
-- appealed_at is the reflux: appeal rate per block and per user is the ASR
-- quality signal §14.3 counts as behaviour data.
ALTER TABLE drill_records
    ADD COLUMN prev_state VARCHAR(16) NOT NULL DEFAULT '' AFTER judge_reason,
    ADD COLUMN prev_success_streak INT NOT NULL DEFAULT 0 AFTER prev_state,
    ADD COLUMN prev_next_due_at DATETIME(3) NULL AFTER prev_success_streak,
    ADD COLUMN appealed_at DATETIME(3) NULL AFTER prev_next_due_at;
