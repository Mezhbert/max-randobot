-- ===========================================================================
-- 0005_scheduler_claim.sql
--
-- ШАГ 2 из 2: колонки и индексы для atomic claim (зависят от enum-значений
-- из 0004, которые теперь закоммичены в отдельной транзакции).
--
-- winners_notified_user_ids — кому из победителей уже отправлено ЛС,
-- для идемпотентного продолжения после краша (не слать повторно).
-- results_published — флаг «итоги уже опубликованы» (обе ветки: edit поста /
-- отдельный пост), отличает повторный прогон finish от первого.
-- ===========================================================================

ALTER TABLE giveaways
    ADD COLUMN IF NOT EXISTS winners_notified_user_ids BIGINT[] NOT NULL DEFAULT '{}';

ALTER TABLE giveaways
    ADD COLUMN IF NOT EXISTS results_published BOOLEAN NOT NULL DEFAULT FALSE;

CREATE INDEX IF NOT EXISTS idx_giveaways_publishing_updated
    ON giveaways (updated_at) WHERE status = 'publishing';
CREATE INDEX IF NOT EXISTS idx_giveaways_finishing_updated
    ON giveaways (updated_at) WHERE status = 'finishing';
