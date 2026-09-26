-- PRD B1: a mini session has a server-enforced turn contract, and the contract
-- has to survive the process that issued it — the gateway resolves it at
-- activation, which is a different process from the one that created the row.
--
-- `standard` is the default so that every existing row and every client that
-- never mentions session_length keeps the behaviour it has today: no cap.
ALTER TABLE practice_sessions
    ADD COLUMN session_length VARCHAR(16) NOT NULL DEFAULT 'standard' COMMENT '会话长度契约：standard | mini（PRD B1）' AFTER scene_type;
