-- V2.x: record whether the user cut this turn off.
--
-- The gateway truncates an interrupted assistant reply to what had been
-- delivered before the barge-in (77_ P1-14): the transcript is a study asset,
-- so it must contain what the user heard, not what the model generated. Without
-- a column the truncated text would be indistinguishable from a reply that was
-- simply that short.
ALTER TABLE utterances
    ADD COLUMN interrupted TINYINT(1) NOT NULL DEFAULT 0;
