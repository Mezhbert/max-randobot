-- ===========================================================================
-- 0002: UTM-трекинг источников подписок + сессии пользователей.
--
-- Механика честного трекинга в MAX (см. архитектуру в README):
--   1) Создатель генерирует диплинк на канал: max.ru/<bot>/start/utm_<code>
--   2) Юзер жмёт → bot_started с payload=utm_<code> → фиксируем клик + атрибуцию
--   3) Юзер вступает в канал (бот админ) → user_added с inviter_id →
--      сопоставляем атрибуцию → инкремент signups
-- ===========================================================================

-- ---------------------------------------------------------------------------
-- user_sessions — произвольные состояния юзера ВНЕ FSM розыгрыша
-- (добавление канала, ожидание ввода % капчи и т.п.).
-- ---------------------------------------------------------------------------
CREATE TABLE user_sessions
(
    user_id    BIGINT PRIMARY KEY REFERENCES users (user_id) ON DELETE CASCADE,
    state      TEXT      NOT NULL DEFAULT '',    -- напр. 'adding_channel', 'await_captcha_pct'
    payload    JSONB     NOT NULL DEFAULT '{}'::jsonb, -- контекст состояния
    updated_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ---------------------------------------------------------------------------
-- utm_links — генерируемые создателем UTM-ссылки на каналы (бот админ).
-- code — короткий уникальный код диплинка: max.ru/<bot>/start/utm_<code>
-- ---------------------------------------------------------------------------
ALTER TABLE utm_links
    ADD COLUMN IF NOT EXISTS owner_user_id BIGINT REFERENCES users (user_id) ON DELETE CASCADE,
    ADD COLUMN IF NOT EXISTS medium       TEXT NOT NULL DEFAULT '',   -- utm_medium (напр. 'post','story')
    ADD COLUMN IF NOT EXISTS post_ref     TEXT NOT NULL DEFAULT '',   -- ссылка/название поста-источника
    ADD COLUMN IF NOT EXISTS unsubs       BIGINT NOT NULL DEFAULT 0,  -- кол-во отписок через эту ссылку
    ADD COLUMN IF NOT EXISTS enabled      BOOLEAN NOT NULL DEFAULT TRUE;

CREATE INDEX IF NOT EXISTS idx_utm_owner ON utm_links (owner_user_id);

-- ---------------------------------------------------------------------------
-- utm_clicks — лог каждого старта бота по UTM-диплинку (клик → бот).
-- ---------------------------------------------------------------------------
CREATE TABLE utm_clicks
(
    id          BIGSERIAL PRIMARY KEY,
    utm_link_id BIGINT REFERENCES utm_links (id) ON DELETE CASCADE,
    user_id     BIGINT NOT NULL,            -- кто кликнул (может быть не в users ещё)
    started_at  TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_utm_clicks_link ON utm_clicks (utm_link_id);
CREATE INDEX idx_utm_clicks_user ON utm_clicks (user_id);

-- ---------------------------------------------------------------------------
-- user_utm_attribution — последний UTM-источник юзера (для атрибуции подписок).
-- Один юзер → один актуальный источник (посний выигрывает).
-- ---------------------------------------------------------------------------
CREATE TABLE user_utm_attribution
(
    user_id     BIGINT PRIMARY KEY REFERENCES users (user_id) ON DELETE CASCADE,
    utm_link_id BIGINT NOT NULL REFERENCES utm_links (id) ON DELETE CASCADE,
    attributed_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_utm_attr_link ON user_utm_attribution (utm_link_id);

-- ---------------------------------------------------------------------------
-- Расширяем subscriptions_log: связываем подписку с UTM-источником.
-- ---------------------------------------------------------------------------
ALTER TABLE subscriptions_log
    ADD COLUMN IF NOT EXISTS utm_link_id BIGINT REFERENCES utm_links (id) ON DELETE SET NULL,
    ADD COLUMN IF NOT EXISTS inviter_id  BIGINT;  -- кто пригласил (из user_added)

-- Триггер touch для новых таблиц.
CREATE TRIGGER trg_user_sessions_touch BEFORE UPDATE ON user_sessions
    FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
