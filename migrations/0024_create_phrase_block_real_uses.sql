-- V2.x: every credited real-world use of a phrase block, with its provenance.
--
-- phrase_blocks.real_use_count is a counter, and until now the only thing that
-- ever incremented it was a B7 in-app hit. A topic-card checkin is the learner
-- confirming they used the phrase in a real conversation — closer to the product's
-- goal than an in-app detection, but a different kind of evidence.
--
-- 86_ M9 named the difference: L1 is what the server observed, L2 is what the
-- user reported. A single counter cannot tell them apart, and a conversion rate
-- that mixes them cannot say which loop is working. Hence a ledger with a source
-- column rather than a second counter.
CREATE TABLE IF NOT EXISTS phrase_block_real_uses (
    id CHAR(36) NOT NULL,
    user_id CHAR(36) NOT NULL,
    block_id CHAR(36) NOT NULL,
    source VARCHAR(16) NOT NULL COMMENT 'hit = B7 应用内命中（L1）；checkin = 用户打卡确认（L2）',
    ref_id VARCHAR(64) NOT NULL COMMENT '来源标识：hit 用 turn_id，checkin 用 card_id',
    used_at DATETIME(3) NOT NULL,
    created_at DATETIME(3) NOT NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uk_pbru_user_block_source_ref (user_id, block_id, source, ref_id),
    KEY idx_pbru_user_used (user_id, used_at),
    KEY idx_pbru_block (block_id),
    CONSTRAINT fk_pbru_user FOREIGN KEY (user_id) REFERENCES users (id),
    CONSTRAINT fk_pbru_block FOREIGN KEY (block_id) REFERENCES phrase_blocks (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
  COMMENT '实战使用账本（带来源，86_ M9/M10）';
