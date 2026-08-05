-- 0006_channel_select_mid.sql
-- mid сообщения с выбором каналов (для edit-in-place при тогглинге галочек).
-- NULL когда создание не на шаге выбора каналов.
ALTER TABLE giveaways ADD COLUMN IF NOT EXISTS channel_select_mid TEXT;
