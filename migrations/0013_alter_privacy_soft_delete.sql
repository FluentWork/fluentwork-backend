-- V2.0 B22 A4: privacy soft-delete columns, tombstones, and support audit log.
-- Numbered 0013 because 0012 is phrase_block_uses unique (B19).
-- materials / reviews tables are owned by B21 / B18 and are skipped here.

ALTER TABLE users
    ADD COLUMN deleted_at DATETIME(3) NULL COMMENT 'A4 soft delete' AFTER updated_at,
    ADD COLUMN tombstone_at DATETIME(3) NULL COMMENT 'A4 tombstone timestamp' AFTER deleted_at,
    ADD KEY idx_users_deleted_at (deleted_at);

ALTER TABLE practice_sessions
    ADD COLUMN deleted_at DATETIME(3) NULL COMMENT 'A4 soft delete' AFTER updated_at,
    ADD KEY idx_practice_sessions_deleted_at (user_id, deleted_at);

ALTER TABLE ai_cost_logs
    ADD COLUMN user_id_anonymized CHAR(36) NULL COMMENT 'original user_id while user_id is NULL' AFTER user_id,
    ADD KEY idx_ai_cost_logs_anonymized (user_id_anonymized);

CREATE TABLE IF NOT EXISTS tombstones (
    id           CHAR(36) NOT NULL,
    user_id      CHAR(36) NOT NULL,
    entity_type  VARCHAR(32) NOT NULL,
    entity_id    CHAR(36) NOT NULL,
    deleted_at   DATETIME(3) NOT NULL,
    purge_at     DATETIME(3) NOT NULL COMMENT 'backup purge deadline (deleted_at + 30d)',
    created_at   DATETIME(3) NOT NULL DEFAULT CURRENT_TIMESTAMP(3),
    PRIMARY KEY (id),
    KEY idx_tombstones_user (user_id, entity_type),
    KEY idx_tombstones_purge (purge_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
  COMMENT 'A4 privacy tombstones (B22)';

CREATE TABLE IF NOT EXISTS audit_logs (
    id         CHAR(36) NOT NULL,
    actor      VARCHAR(128) NOT NULL,
    action     VARCHAR(64) NOT NULL,
    user_id    CHAR(36) NOT NULL,
    reason     VARCHAR(255) NULL,
    created_at DATETIME(3) NOT NULL,
    PRIMARY KEY (id),
    KEY idx_audit_logs_user (user_id, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci
  COMMENT 'support undelete audit (B22 T-B22-8)';
