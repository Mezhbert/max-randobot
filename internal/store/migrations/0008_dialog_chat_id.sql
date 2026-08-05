-- 0008_dialog_chat_id.sql
-- chat_id диалога bot↔user (нужен для GetMessages при /clear).
-- MAX API для получения сообщений требует chat_id, не user_id.
ALTER TABLE users ADD COLUMN IF NOT EXISTS dialog_chat_id BIGINT;
