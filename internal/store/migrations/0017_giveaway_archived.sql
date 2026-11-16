-- Архив розыгрышей: завершённые можно скрыть из списка (archived_at),
-- а также удалить навсегда (FK участников — ON DELETE CASCADE).
ALTER TABLE giveaways ADD COLUMN IF NOT EXISTS archived_at TIMESTAMPTZ NULL;
CREATE INDEX IF NOT EXISTS idx_giveaways_owner_archived ON giveaways (owner_user_id, archived_at);
