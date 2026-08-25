-- Practice session baseline for B2 (POST /sessions).
-- material_id is opaque for now; materials table lands with the materials module.
-- session_tickets stores one-time WSS tickets (60s) for gateway validation in B3.

CREATE TABLE IF NOT EXISTS practice_sessions (
    id CHAR(36) NOT NULL,
    user_id CHAR(36) NOT NULL,
    material_id CHAR(36) NULL,
    scene_type VARCHAR(64) NOT NULL DEFAULT 'demo',
    status VARCHAR(32) NOT NULL DEFAULT 'created',
    duration_sec INT NOT NULL DEFAULT 0,
    review_json JSON NULL,
    created_at DATETIME(3) NOT NULL,
    updated_at DATETIME(3) NOT NULL,
    PRIMARY KEY (id),
    KEY idx_practice_sessions_user_created (user_id, created_at),
    KEY idx_practice_sessions_status (status),
    CONSTRAINT fk_practice_sessions_user FOREIGN KEY (user_id) REFERENCES users (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS session_tickets (
    id CHAR(36) NOT NULL,
    session_id CHAR(36) NOT NULL,
    user_id CHAR(36) NOT NULL,
    token_hash CHAR(64) NOT NULL,
    expires_at DATETIME(3) NOT NULL,
    used_at DATETIME(3) NULL,
    created_at DATETIME(3) NOT NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uk_session_tickets_token_hash (token_hash),
    KEY idx_session_tickets_session_id (session_id),
    CONSTRAINT fk_session_tickets_session FOREIGN KEY (session_id) REFERENCES practice_sessions (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
