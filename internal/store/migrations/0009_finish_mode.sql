-- 0009_finish_mode.sql
-- Способ подведения итогов: по времени (time) или по количеству участников (count).
-- finish_participants_count — целевое число участников для finish_mode='count'.
ALTER TABLE giveaways ADD COLUMN IF NOT EXISTS finish_mode TEXT NOT NULL DEFAULT 'time';
ALTER TABLE giveaways ADD COLUMN IF NOT EXISTS finish_participants_count INT NOT NULL DEFAULT 0;

-- Индекс для быстрого поиска розыгрышей, готовых к завершению по количеству.
CREATE INDEX IF NOT EXISTS idx_giveaways_finish_count ON giveaways (status)
    WHERE finish_mode = 'count' AND finish_participants_count > 0;
