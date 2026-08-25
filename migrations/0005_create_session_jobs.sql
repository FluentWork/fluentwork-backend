-- Async session.finished outbox for B5 review worker (Redis Stream can replace later).

CREATE TABLE IF NOT EXISTS session_jobs (
    id CHAR(36) NOT NULL,
    session_id CHAR(36) NOT NULL,
    job_type VARCHAR(64) NOT NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'pending',
    attempts INT NOT NULL DEFAULT 0,
    available_at DATETIME(3) NOT NULL,
    locked_at DATETIME(3) NULL,
    locked_by VARCHAR(128) NULL,
    last_error TEXT NULL,
    created_at DATETIME(3) NOT NULL,
    updated_at DATETIME(3) NOT NULL,
    PRIMARY KEY (id),
    KEY idx_session_jobs_claim (status, available_at, created_at),
    KEY idx_session_jobs_session (session_id),
    CONSTRAINT fk_session_jobs_session FOREIGN KEY (session_id) REFERENCES practice_sessions (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
