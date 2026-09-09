-- Rollback for 0013_alter_privacy_soft_delete.sql.
-- Keep this file out of migrations/*.sql so local-db-init does not apply it.

DROP TABLE IF EXISTS audit_logs;
DROP TABLE IF EXISTS tombstones;

ALTER TABLE ai_cost_logs
    DROP KEY idx_ai_cost_logs_anonymized,
    DROP COLUMN user_id_anonymized;

ALTER TABLE practice_sessions
    DROP KEY idx_practice_sessions_deleted_at,
    DROP COLUMN deleted_at;

ALTER TABLE users
    DROP KEY idx_users_deleted_at,
    DROP COLUMN tombstone_at,
    DROP COLUMN deleted_at;
