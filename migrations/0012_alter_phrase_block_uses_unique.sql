-- V2.0: Unique key so POST /internal/v1/voicegateway/hits can UPSERT one
-- ledger row per (session, turn, block) and increment total_uses only once.
-- Per B19 T-HIT-2 (docs/48_ §2.6). Leaves 0011 drill_records unchanged.
-- B22 privacy migrations should continue at 0013.

ALTER TABLE phrase_block_uses
    ADD UNIQUE KEY uk_pbu_session_turn_block (session_id, turn_id, block_id);
