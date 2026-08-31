-- Phrase blocks for B10 corpus module.
-- anchor_user_said is stored plaintext by design so corpus search and later hit-detect lookup can work.

CREATE TABLE IF NOT EXISTS phrase_blocks (
    id CHAR(36) NOT NULL,
    user_id CHAR(36) NOT NULL,
    intent_zh VARCHAR(255) NOT NULL,
    expression_en VARCHAR(512) NOT NULL,
    anchor_user_said VARCHAR(512) NOT NULL,
    scene_tag VARCHAR(32) NOT NULL,
    function_tag VARCHAR(32) NOT NULL,
    state VARCHAR(16) NOT NULL DEFAULT 'new',
    success_streak INT NOT NULL DEFAULT 0,
    next_due_at DATETIME(3) NOT NULL,
    ease_factor DOUBLE NOT NULL DEFAULT 2.5,
    real_use_count INT NOT NULL DEFAULT 0,
    is_favorite TINYINT(1) NOT NULL DEFAULT 0,
    pinned_at DATETIME(3) NULL,
    source_session_id CHAR(36) NULL,
    deleted_at DATETIME(3) NULL,
    created_at DATETIME(3) NOT NULL,
    updated_at DATETIME(3) NOT NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uk_phrase_blocks_user_source_expr_anchor (
        user_id, source_session_id, expression_en, anchor_user_said, scene_tag, function_tag
    ),
    KEY idx_phrase_blocks_user_due (user_id, next_due_at),
    KEY idx_phrase_blocks_user_scene (user_id, scene_tag),
    KEY idx_phrase_blocks_user_func (user_id, function_tag),
    KEY idx_phrase_blocks_user_fav (user_id, is_favorite, pinned_at),
    KEY idx_phrase_blocks_user_created (user_id, created_at),
    FULLTEXT KEY ft_phrase_blocks_expr_intent (expression_en, intent_zh),
    CONSTRAINT fk_phrase_blocks_user FOREIGN KEY (user_id) REFERENCES users (id),
    CONSTRAINT fk_phrase_blocks_session FOREIGN KEY (source_session_id) REFERENCES practice_sessions (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
