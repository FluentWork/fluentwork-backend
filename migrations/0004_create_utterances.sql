-- Utterances for B4 session.end persistence.
-- Text is stored plaintext in the first wave; field-level encryption lands with the privacy hardening pass.

CREATE TABLE IF NOT EXISTS utterances (
    id CHAR(36) NOT NULL,
    session_id CHAR(36) NOT NULL,
    seq INT NOT NULL,
    speaker VARCHAR(16) NOT NULL,
    text MEDIUMTEXT NOT NULL,
    asr_confidence DOUBLE NULL,
    audio_url VARCHAR(1024) NULL,
    hit_block_ids JSON NULL,
    pron_score DOUBLE NULL,
    pron_detail JSON NULL,
    created_at DATETIME(3) NOT NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uk_utterances_session_seq (session_id, seq),
    KEY idx_utterances_session_created (session_id, created_at),
    CONSTRAINT fk_utterances_session FOREIGN KEY (session_id) REFERENCES practice_sessions (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
