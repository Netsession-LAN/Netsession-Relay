-- Switching a game off keeps its row (profile, overrides, source filter) and only clears this flag, so the
-- settings are back when it is switched on again. Rows without the flag were all switched on.

ALTER TABLE event_game ADD COLUMN enabled INTEGER NOT NULL DEFAULT 1;
