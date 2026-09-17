-- V2.x B8 → D1: rescue events as refine's second input (PRD §5.4.4 / §5.2.2).
--
-- The rescue ladder is a source of phrase blocks, not just a repair: an
-- unfinished sentence contributes the user's half-sentence as the anchor, and a
-- silent turn contributes the first sentence the user managed after the ladder.
-- Neither survives in the transcript alone, so the gateway reports the ladder
-- itself and app-server keeps it next to the utterances it belongs to.
--
-- A rescue where the user never spoke produces no phrase block (`anchor_text`
-- stays empty) but is still recorded: level and duration feed scene difficulty
-- and material selection.
CREATE TABLE IF NOT EXISTS rescue_events (
    id CHAR(36) NOT NULL,
    session_id CHAR(36) NOT NULL,
    user_id CHAR(36) NOT NULL,
    seq INT NOT NULL,
    turn_id VARCHAR(64) NOT NULL DEFAULT '',
    level TINYINT NOT NULL DEFAULT 1,
    path VARCHAR(16) NOT NULL,
    ladder_text VARCHAR(512) NOT NULL DEFAULT '',
    user_opened TINYINT(1) NOT NULL DEFAULT 0,
    anchor_text VARCHAR(512) NOT NULL DEFAULT '',
    created_at DATETIME(3) NOT NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uk_rescue_events_session_seq (session_id, seq),
    KEY idx_rescue_events_user_created (user_id, created_at),
    CONSTRAINT fk_rescue_events_session FOREIGN KEY (session_id) REFERENCES practice_sessions (id),
    CONSTRAINT fk_rescue_events_user FOREIGN KEY (user_id) REFERENCES users (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
