-- Account baseline from backend technical design §3.1 / §3.2.5.
-- Guest rows use is_guest=1 and device_id; merge moves device_id onto the
-- registered user and marks the guest status=merged without physical delete.

CREATE TABLE IF NOT EXISTS users (
    id CHAR(36) NOT NULL,
    email VARCHAR(255) NULL,
    phone VARCHAR(32) NULL,
    device_id VARCHAR(128) NULL,
    is_guest TINYINT(1) NOT NULL DEFAULT 1,
    pwd_hash VARCHAR(255) NULL,
    status VARCHAR(32) NOT NULL DEFAULT 'active',
    merged_into_user_id CHAR(36) NULL,
    created_at DATETIME(3) NOT NULL,
    updated_at DATETIME(3) NOT NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uk_users_email (email),
    UNIQUE KEY uk_users_phone (phone),
    UNIQUE KEY uk_users_device_id (device_id),
    KEY idx_users_merged_into (merged_into_user_id),
    CONSTRAINT fk_users_merged_into FOREIGN KEY (merged_into_user_id) REFERENCES users (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;

CREATE TABLE IF NOT EXISTS auth_tokens (
    id CHAR(36) NOT NULL,
    user_id CHAR(36) NOT NULL,
    refresh_token_hash CHAR(64) NOT NULL,
    expires_at DATETIME(3) NOT NULL,
    created_at DATETIME(3) NOT NULL,
    PRIMARY KEY (id),
    UNIQUE KEY uk_auth_tokens_refresh_hash (refresh_token_hash),
    KEY idx_auth_tokens_user_id (user_id),
    CONSTRAINT fk_auth_tokens_user FOREIGN KEY (user_id) REFERENCES users (id)
) ENGINE=InnoDB DEFAULT CHARSET=utf8mb4 COLLATE=utf8mb4_0900_ai_ci;
