-- Cost telemetry table required by backend technical design §3.2.3.
-- Write path is wired when AI calls land; the table must exist from W1.

CREATE TABLE IF NOT EXISTS ai_cost_logs (
    id CHAR(36) NOT NULL,
    user_id CHAR(36) NULL,
    task_type VARCHAR(64) NOT NULL,
    model VARCHAR(128) NOT NULL DEFAULT '',
    tokens_in INT NOT NULL DEFAULT 0,
    tokens_out INT NOT NULL DEFAULT 0,
    audio_sec INT NOT NULL DEFAULT 0,
    cost_fen INT NOT NULL DEFAULT 0,
    created_at DATETIME(3) NOT NULL,
    PRIMARY KEY (id),
    KEY idx_user_date (user_id, created_at)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
