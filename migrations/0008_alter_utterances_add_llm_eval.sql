-- V2.0: Add llm_eval_json to utterances (B8 review eval storage).
-- Per docs/30_技术方案/39_FluentWork后端技术架构设计V2_0_2026-09-03.md §7.1
-- Per docs/30_技术方案/48_FluentWork_V2_REST接口契约冻结_2026-09-06.md §1.3.3

ALTER TABLE utterances
    ADD COLUMN llm_eval_json JSON NULL COMMENT 'LLM 评价结果 (V2.0 B8 真实评价)' AFTER hit_block_ids;
