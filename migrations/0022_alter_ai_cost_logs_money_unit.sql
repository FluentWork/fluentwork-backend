-- V2.x: the money column moves from 分 to 微元 (10^-6 元).
--
-- Why, before any real rate was ever entered: a model priced under 10 CNY per
-- million tokens costs less than one 分 for a single call — 0.8 CNY/1M means a
-- 1000+500 token call is 0.12 分. Integer 分 per 1K tokens cannot express that,
-- so every call rounded to the 1-分 floor and the column became "calls × 1 分",
-- a number that looks like money and is not.
--
-- Nothing is migrated: the values in the old column were estimates computed from
-- a stale price table (wrong model family, and off by 100x against its own
-- comment), so they are discarded rather than carried forward at a new unit and
-- given a false precision.
ALTER TABLE ai_cost_logs
    CHANGE COLUMN cost_fen cost_micro_yuan BIGINT NOT NULL DEFAULT 0
    COMMENT '费用，单位微元 (10^-6 元)。费率为 0 表示尚未核实（voice 行）或用量未定价';
