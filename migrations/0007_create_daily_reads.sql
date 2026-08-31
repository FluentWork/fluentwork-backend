-- Daily reads for B11 content module.

CREATE TABLE IF NOT EXISTS daily_reads (
    id CHAR(36) NOT NULL,
    user_id CHAR(36) NOT NULL,
    gen_date DATE NOT NULL,
    status VARCHAR(16) NOT NULL DEFAULT 'pending',
    title VARCHAR(255) NOT NULL DEFAULT '',
    body MEDIUMTEXT NOT NULL,
    audio_url VARCHAR(512) NULL,
    used_block_ids JSON NULL,
    source_refs JSON NULL,
    read_score DOUBLE NULL,
    generator VARCHAR(64) NULL,
    created_at DATETIME(3) NOT NULL,
    updated_at DATETIME(3) NOT NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uk_daily_reads_user_date (user_id, gen_date),
    KEY idx_daily_reads_user_status (user_id, status, gen_date),
    CONSTRAINT fk_daily_reads_user FOREIGN KEY (user_id) REFERENCES users (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
