-- V2.0: Create drill_records (E1/E2 drill attempt ledger).
-- Per docs/30_技术方案/39_FluentWork后端技术架构设计V2_0_2026-09-03.md §7.3
-- Per docs/30_技术方案/48_FluentWork_V2_REST接口契约冻结_2026-09-06.md §1.5
--
-- One row per drill attempt. pronunciation_score is V1.1 (NULL for V2.0 MVP).
-- drill_type follows E1/E2/E3 enum: 1=层级1召回闪测 2=换说法训练 3=概念封装.
-- asr_text is capped at VARCHAR(500) (PRD flash card speak length).

CREATE TABLE IF NOT EXISTS drill_records (
    id              BIGINT UNSIGNED AUTO_INCREMENT PRIMARY KEY,
    user_id         CHAR(36) NOT NULL,
    block_id        CHAR(36) NOT NULL,
    session_id      CHAR(36) NULL COMMENT '关联练习会话 (可为 NULL, 即便不是从会话进入)',
    drill_type      TINYINT NOT NULL COMMENT '1=层级1召回闪测 2=换说法训练 3=概念封装',
    semantic_pass   TINYINT(1) NOT NULL COMMENT 'E2: 语义等价判定结果',
    pronunciation_score DECIMAL(3,1) NULL COMMENT '发音分 (V1.1 启用, MVP=NULL)',
    response_ms     INT UNSIGNED NOT NULL COMMENT '用户响应耗时 ms',
    asr_text        VARCHAR(500) NOT NULL COMMENT '用户作答 ASR 文本',
    judge_reason    VARCHAR(200) NULL COMMENT '判定理由',
    created_at      DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),

    INDEX idx_dr_block (block_id, created_at),
    INDEX idx_dr_user (user_id, created_at),
    INDEX idx_dr_due (user_id, drill_type, created_at),
    CONSTRAINT fk_dr_user FOREIGN KEY (user_id) REFERENCES users (id),
    CONSTRAINT fk_dr_block FOREIGN KEY (block_id) REFERENCES phrase_blocks (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
  COMMENT '闪测记录 (E1/E2 39 §7.3)';
