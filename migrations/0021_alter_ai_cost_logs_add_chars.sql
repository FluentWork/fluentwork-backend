-- V2.x P1-5: TTS bills by characters, so the ledger needs a column for them.
--
-- The other units already have honest columns: token counts for LLM calls,
-- audio_sec for the duplex stream. Reusing either for TTS characters would put
-- a number under a label that does not mean it — the failure mode 51_ §4.3
-- warns about ("账本里一个按猜测算出的数字看起来权威").
--
-- Rows written before this column exist keep chars = 0, which is exactly what
-- they recorded: nothing was billed by character then.
ALTER TABLE ai_cost_logs
    ADD COLUMN chars INT NOT NULL DEFAULT 0 COMMENT 'TTS 计费字符数 (voice.tts)' AFTER audio_sec;
