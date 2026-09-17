-- V2.x: an attempt the judge could not score is not a failed attempt.
--
-- The drill judge runs under a latency budget; when it expires, the LLM seam
-- returns "judge_timeout" with no error, and the service used to apply the
-- failure ladder: streak zeroed, block demoted, next_due +1h. A learner who
-- answered correctly was told they were wrong — the exact failure E2's appeal
-- exists to undo, applied to every attempt.
--
-- This column keeps the ledger honest: the attempt happened and is counted
-- (it still spends a daily new-block release), but it carries no verdict.
ALTER TABLE drill_records
    ADD COLUMN judged TINYINT(1) NOT NULL DEFAULT 1 COMMENT '0 = 判定未跑成（超时/解析失败），本条不含判定结论' AFTER semantic_pass;
