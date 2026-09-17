-- V2.x refine-quality reflux (83_ §2.2 风险 1: 炼化的话术块质量参差不齐).
--
-- The "not idiomatic enough" button is the only signal that tells prompt work
-- whether a rewrite landed. It is stored per (user, block, reason) rather than
-- as a flag on the block: the same learner may dislike one card for two
-- different reasons, and the aggregate is what prompt iteration reads.
--
-- Soft-delete columns match the rest of the corpus so A4's wipe covers it.
CREATE TABLE IF NOT EXISTS phrase_block_feedback (
    id CHAR(36) NOT NULL,
    user_id CHAR(36) NOT NULL,
    block_id CHAR(36) NOT NULL,
    reason VARCHAR(32) NOT NULL COMMENT 'not_idiomatic | not_useful | wrong_meaning',
    deleted_at DATETIME(3) NULL,
    created_at DATETIME(3) NOT NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uk_feedback_user_block_reason (user_id, block_id, reason),
    KEY idx_feedback_user_created (user_id, created_at),
    KEY idx_feedback_block (block_id),
    CONSTRAINT fk_feedback_user FOREIGN KEY (user_id) REFERENCES users (id),
    CONSTRAINT fk_feedback_block FOREIGN KEY (block_id) REFERENCES phrase_blocks (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
