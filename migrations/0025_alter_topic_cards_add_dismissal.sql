-- V2.x: why a topic card did not turn into a real conversation (86_ M11).
--
-- Until now the product only heard about the cards that worked: a checkin means
-- somebody talked to somebody. The cards that produced nothing were silent, and
-- the reason matters — "I could not find anyone to speak English with" argues
-- for in-app simulated partners, "I did not feel confident enough" argues for a
-- lower first step, "no time" argues for shorter cards, and "not relevant"
-- argues that the topic generation is still off.
--
-- A dismissal is not a failure and carries no penalty: the learner is telling us
-- why the last mile is hard, which is the most actionable thing this product can
-- learn about 实战出口 (§14.3 维度五).
ALTER TABLE topic_cards
    ADD COLUMN dismissed_at DATETIME(3) NULL COMMENT '标记"这次没聊成"的时间' AFTER valid_until,
    ADD COLUMN dismiss_reason VARCHAR(32) NOT NULL DEFAULT '' COMMENT 'no_partner|not_confident|no_time|not_relevant' AFTER dismissed_at;
