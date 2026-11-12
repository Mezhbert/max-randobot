-- 0015_click_membership — первичная проверка подписки В МОМЕНТ клика
-- (вместо суточного поллинга): если кликнувший уже участник канала —
-- пользователь_added для него никогда не придёт, фиксируем сразу
-- отдельным флагом (полезная метрика «уже были участниками»).
-- Дальнейшие подписки/отписки — событиями user_added/user_removed.

ALTER TABLE utm_subscribers
    ADD COLUMN IF NOT EXISTS was_subscribed_at_click BOOLEAN NOT NULL DEFAULT FALSE;
