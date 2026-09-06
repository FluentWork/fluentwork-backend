-- V2.0: Add total_uses and last_used_at to phrase_blocks (B7 hit signal feedback).
-- Per docs/30_技术方案/44_B7_LLM注入完整接口契约_2026-09-03.md §4.2
-- Per docs/30_技术方案/48_FluentWork_V2_REST接口契约冻结_2026-09-06.md §2.6
--
-- Note: real_use_count already exists (0006). The new total_uses is the
-- app-server-side aggregate, distinct from real_use_count which is per-session
-- recovery metric. last_used_at drives phrase-block recency ordering for
-- corpus list API.

ALTER TABLE phrase_blocks
    ADD COLUMN total_uses INT NOT NULL DEFAULT 0 COMMENT '实战命中累计 (B7),由 0010 同步' AFTER real_use_count,
    ADD COLUMN last_used_at DATETIME(3) NULL COMMENT '最近一次实战命中时间' AFTER total_uses;
