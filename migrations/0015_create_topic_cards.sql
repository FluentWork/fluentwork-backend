-- V2.0 B23 topic cards, checkins, and streaks (H1/H2/H3).

CREATE TABLE IF NOT EXISTS topic_cards (
    id CHAR(36) NOT NULL,
    user_id CHAR(36) NOT NULL,
    for_date DATE NOT NULL,
    title VARCHAR(255) NOT NULL,
    prompt_en TEXT NOT NULL,
    prompt_zh TEXT NOT NULL,
    card_type VARCHAR(32) NOT NULL,
    seed_tags JSON NULL,
    valid_until DATETIME(3) NOT NULL,
    checked_in_at DATETIME(3) NULL,
    deleted_at DATETIME(3) NULL,
    created_at DATETIME(3) NOT NULL,
    updated_at DATETIME(3) NOT NULL,
    PRIMARY KEY (id),
    KEY idx_topic_cards_user_date (user_id, for_date, deleted_at),
    CONSTRAINT fk_topic_cards_user FOREIGN KEY (user_id) REFERENCES users (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS topic_checkins (
    id CHAR(36) NOT NULL,
    card_id CHAR(36) NOT NULL,
    user_id CHAR(36) NOT NULL,
    reflection VARCHAR(512) NOT NULL DEFAULT '',
    deleted_at DATETIME(3) NULL,
    created_at DATETIME(3) NOT NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uk_topic_checkins_card (card_id),
    KEY idx_topic_checkins_user (user_id, created_at),
    CONSTRAINT fk_topic_checkins_card FOREIGN KEY (card_id) REFERENCES topic_cards (id),
    CONSTRAINT fk_topic_checkins_user FOREIGN KEY (user_id) REFERENCES users (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS topic_streaks (
    user_id CHAR(36) NOT NULL,
    current_streak INT NOT NULL DEFAULT 0,
    longest_streak INT NOT NULL DEFAULT 0,
    last_checkin_date DATE NULL,
    deleted_at DATETIME(3) NULL,
    updated_at DATETIME(3) NOT NULL,
    PRIMARY KEY (user_id),
    CONSTRAINT fk_topic_streaks_user FOREIGN KEY (user_id) REFERENCES users (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
