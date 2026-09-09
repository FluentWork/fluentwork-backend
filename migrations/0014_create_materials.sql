-- V2.0 B21 materials (A1/A2): paste / sentence / URL → async refine into phrase_blocks.

CREATE TABLE IF NOT EXISTS materials (
    id CHAR(36) NOT NULL,
    user_id CHAR(36) NOT NULL,
    kind VARCHAR(32) NOT NULL,
    content MEDIUMTEXT NOT NULL,
    refine_status VARCHAR(32) NOT NULL DEFAULT 'queued',
    block_count INT NOT NULL DEFAULT 0,
    error_code VARCHAR(64) NULL,
    deleted_at DATETIME(3) NULL,
    created_at DATETIME(3) NOT NULL,
    updated_at DATETIME(3) NOT NULL,
    PRIMARY KEY (id),
    KEY idx_materials_user_created (user_id, created_at),
    KEY idx_materials_user_deleted (user_id, deleted_at),
    CONSTRAINT fk_materials_user FOREIGN KEY (user_id) REFERENCES users (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
