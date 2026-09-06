-- V2.0: Create phrase_block_uses (B7 hit signal ledger).
-- Per docs/30_技术方案/44_B7_LLM注入完整接口契约_2026-09-03.md §4.1
-- Per docs/30_技术方案/48_FluentWork_V2_REST接口契约冻结_2026-09-06.md §2.6
--
-- This table is written by app-server's POST /internal/v1/voicegateway/hits
-- handler. A trigger (or service-layer write) syncs phrase_blocks.total_uses
-- and last_used_at on each insert. Service-layer write is preferred
-- (avoid trigger complexity in review).

CREATE TABLE IF NOT EXISTS phrase_block_uses (
    id              BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    user_id         CHAR(36) NOT NULL,
    session_id      CHAR(36) NOT NULL,
    turn_id         CHAR(36) NOT NULL,
    block_id        CHAR(36) NOT NULL,
    used_at_ms      BIGINT NOT NULL,
    created_at      DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    INDEX idx_pbu_user_block (user_id, block_id, used_at_ms),
    INDEX idx_pbu_session_turn (session_id, turn_id),
    INDEX idx_pbu_block_recent (block_id, used_at_ms DESC),
    CONSTRAINT fk_pbu_user FOREIGN KEY (user_id) REFERENCES users (id),
    CONSTRAINT fk_pbu_block FOREIGN KEY (block_id) REFERENCES phrase_blocks (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
  COMMENT 'B7 实战命中记录 (44 §4.1)';
