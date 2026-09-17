-- V2.x H1/H2: topic cards must be grounded in the learner's own corpus.
--
-- PRD §7.8 H1 asks each card to carry the phrase blocks it can draw on (可调用
-- 话术块清单), and H2 forbids generic topics: cards come from the user's own
-- materials and corpus tags, and each card names its source. Without these
-- columns the server cannot tell a grounded card from a plausible-looking
-- generic one — the exact failure §14.3 warns about ("另一个豆包").
ALTER TABLE topic_cards
    ADD COLUMN block_ids JSON NULL COMMENT '可调用话术块 id 清单 (H1)' AFTER seed_tags,
    ADD COLUMN source_note VARCHAR(255) NOT NULL DEFAULT '' COMMENT '来源标注 (H2)' AFTER block_ids;
