-- Rollback for 0016_alter_utterances_add_interrupted.sql.

ALTER TABLE utterances
    DROP COLUMN interrupted;
