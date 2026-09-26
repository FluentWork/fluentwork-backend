-- V2.x BE-S1-1: `processing` had no exit, so a crashed refine stranded the material.
--
-- Refine runs in-process: POST /materials inserts a queued row, a goroutine takes
-- it queued -> processing -> ready|failed, and nothing else ever touches it. So if
-- the process dies mid-refine (or the LLM call hangs past its deadline), the row
-- stays `processing` forever. `Refine` no-ops on a non-queued row, so a retry
-- cannot even be asked for. The user pasted material, the card never appears, and
-- nothing anywhere says why.
--
-- `session_jobs` already solved this shape (0005, DefaultJobLease): the claim
-- writes locked_at, and a later pass may take the row back once the lease expires.
-- `attempts` is what turns that into a bounded story — initial try plus one retry,
-- then `failed` with lease_expired, which is an exit the user can see instead of a
-- spinner that never resolves.
--
-- No locked_by: refine has no worker identity to record (it is the app-server
-- process itself), and a column that can only ever hold one constant would be a
-- field that looks like it carries information and does not.
ALTER TABLE materials
    ADD COLUMN attempts INT NOT NULL DEFAULT 0 COMMENT 'refine 尝试次数（含首次，BE-S1-1）' AFTER refine_status,
    ADD COLUMN locked_at DATETIME(3) NULL COMMENT '本次 refine 的租约起点；过期即可被清扫回收（BE-S1-1）' AFTER attempts,
    ADD KEY idx_materials_refine_sweep (refine_status, locked_at);
