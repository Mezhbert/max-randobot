-- 0012: счётчик неудачных попыток публикации. После N ретраев (канал недоступен,
-- бот кикнут и т.п.) розыгрыш переводится в cancelled с уведомлением владельцу —
-- вместо бесконечного цикла claim/fail/revert каждые 30 секунд.
ALTER TABLE giveaways
    ADD COLUMN IF NOT EXISTS publish_attempts INT NOT NULL DEFAULT 0;
