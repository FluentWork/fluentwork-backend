-- V2.x: an edited expression is a different sentence to recall (86_ M6).
--
-- expression_en is what the learner has to produce from the Chinese cue. Editing
-- it changes the task, so the recall evidence gathered for the old wording says
-- nothing about the new one: leaving the streak and the green light in place
-- would claim "this learner can say this fluently" about a sentence they have
-- never said. The schedule therefore resets, and the edit is recorded so a
-- reader can tell that a block's history spans versions.
--
-- Intent, anchor, scene and function tags are editable without a reset: they
-- refine the cue and the provenance, not the phrase being recalled.
ALTER TABLE phrase_blocks
    ADD COLUMN expression_version INT NOT NULL DEFAULT 1
        COMMENT 'expression_en 的第几版（编辑即 +1，86_ M6）' AFTER expression_en;

CREATE TABLE IF NOT EXISTS phrase_block_edits (
    id CHAR(36) NOT NULL,
    user_id CHAR(36) NOT NULL,
    block_id CHAR(36) NOT NULL,
    version INT NOT NULL,
    old_expression VARCHAR(512) NOT NULL,
    new_expression VARCHAR(512) NOT NULL,
    created_at DATETIME(3) NOT NULL,
    PRIMARY KEY (id),
    KEY idx_pbe_block_created (block_id, created_at),
    KEY idx_pbe_user_created (user_id, created_at),
    CONSTRAINT fk_pbe_user FOREIGN KEY (user_id) REFERENCES users (id),
    CONSTRAINT fk_pbe_block FOREIGN KEY (block_id) REFERENCES phrase_blocks (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
  COMMENT '话术块表达编辑历史（86_ M6）';
