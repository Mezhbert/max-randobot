-- 0014_tracking_retention — полный UTM-билдер + удержание подписчиков.
--
-- Билдер (все поля опциональны, скип = как без билдера):
--   utm_term / utm_content — остальные метки из стандартной пятёрки UTM
--     (utm_source = название ссылки, utm_medium, utm_campaign уже есть);
--   giveaway_id — привязка ссылки к конкретному розыгрышу: в аналитике видно,
--     сколько подписчиков ОСТАЛОСЬ после окончания розыгрыша.
--
-- Удержание (retention): кто из кликнувших подписался и когда отписался.
-- Наполняется тремя путями:
--   1) user_added с атрибуцией → подписка (реальное время);
--   2) user_removed с атрибуцией → отписка (реальное время);
--   3) суточный поллинг IsSubscriber (бот админ канала) в течение 14 дней —
--      ловит тихие отписки и ДОБИРАЕТ уже-состоящих на момент клика
--      (владелец канала, подписанные ранее — user_added для них не придёт).

ALTER TABLE utm_links
    ADD COLUMN IF NOT EXISTS utm_term TEXT NOT NULL DEFAULT '';
ALTER TABLE utm_links
    ADD COLUMN IF NOT EXISTS utm_content TEXT NOT NULL DEFAULT '';
ALTER TABLE utm_links
    ADD COLUMN IF NOT EXISTS giveaway_id BIGINT REFERENCES giveaways (id) ON DELETE SET NULL;

CREATE TABLE IF NOT EXISTS utm_subscribers (
    id              BIGSERIAL PRIMARY KEY,
    utm_link_id     BIGINT NOT NULL REFERENCES utm_links (id) ON DELETE CASCADE,
    user_id         BIGINT NOT NULL REFERENCES users (user_id) ON DELETE CASCADE,
    chat_id         BIGINT NOT NULL,                 -- канал (chat_id MAX), проверяется поллингом
    subscribed_at   TIMESTAMPTZ NOT NULL DEFAULT now(), -- момент подписки/первого обнаружения
    unsubscribed_at TIMESTAMPTZ,                     -- NULL = подписан (на последнюю проверку)
    last_checked_at TIMESTAMPTZ,                     -- последняя суточная проверка
    UNIQUE (utm_link_id, user_id)
);

CREATE INDEX IF NOT EXISTS idx_utm_subs_link ON utm_subscribers (utm_link_id);
CREATE INDEX IF NOT EXISTS idx_utm_subs_user ON utm_subscribers (user_id);
