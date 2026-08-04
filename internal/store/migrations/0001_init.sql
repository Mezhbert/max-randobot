-- ===========================================================================
-- Начальная схема БД MAX-бота для розыгрышей.
-- Все таймстампы — timestamptz (хранятся в UTC). Логика "по МСК" — в коде,
-- через Europe/Moscow при парсинге дат от пользователя.
-- ===========================================================================

-- enum'ы статусов розыгрыша:
--   draft     — в процессе создания (черновик)
--   scheduled — запланированный (publish_at в будущем)
--   active    — текущий (опубликован, идёт приём участников)
--   finished  — завершён (итоги подведены)
--   cancelled — отменён создателем
CREATE TYPE giveaway_status AS ENUM
    ('draft', 'scheduled', 'active', 'finished', 'cancelled');

CREATE TYPE captcha_mode AS ENUM ('none', 'all', 'percent');

CREATE TYPE post_format AS ENUM ('', 'markdown', 'html');

-- ---------------------------------------------------------------------------
-- Пользователи бота (все, кто хоть раз написал боту / участвовал).
-- flag_in_base: может ли бот отправлять этому пользователю сообщение
--               (false, если юзер заблокировал бота / остановил диалог).
-- ---------------------------------------------------------------------------
CREATE TABLE users
(
    user_id           BIGINT PRIMARY KEY,
    first_name        TEXT        NOT NULL DEFAULT '',
    username          TEXT        NOT NULL DEFAULT '',
    is_bot            BOOLEAN     NOT NULL DEFAULT FALSE,
    flag_in_base      BOOLEAN     NOT NULL DEFAULT TRUE,
    created_at        TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at        TIMESTAMPTZ NOT NULL DEFAULT now()
);

-- ---------------------------------------------------------------------------
-- Каналы создателя ("мои каналы"). Бот должен быть админом в каждом канале,
-- который фигурирует в проверке подписки / публикации.
-- chat_id — числовой идентификатор чата/канала в MAX.
-- ---------------------------------------------------------------------------
CREATE TABLE channels
(
    id              BIGSERIAL PRIMARY KEY,
    owner_user_id   BIGINT      NOT NULL REFERENCES users (user_id) ON DELETE CASCADE,
    chat_id         BIGINT      NOT NULL,
    title           TEXT        NOT NULL DEFAULT '',
    link            TEXT        NOT NULL DEFAULT '',
    is_public       BOOLEAN     NOT NULL DEFAULT TRUE,
    is_bot_admin    BOOLEAN     NOT NULL DEFAULT FALSE, -- кэш проверки GetMembership
    admin_checked_at TIMESTAMPTZ,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (owner_user_id, chat_id)
);

CREATE INDEX idx_channels_owner ON channels (owner_user_id);

-- ---------------------------------------------------------------------------
-- Розыгрыши. Поля соответствуют этапам 1-13 создания.
-- ---------------------------------------------------------------------------
CREATE TABLE giveaways
(
    id                         BIGSERIAL PRIMARY KEY,
    owner_user_id              BIGINT          NOT NULL REFERENCES users (user_id) ON DELETE CASCADE,

    -- Этап 3: название конкурса (в посте не показывается, только для создателя)
    name                       TEXT            NOT NULL DEFAULT '',

    -- Этап 1: текст поста + формат оформления (как у юзера)
    post_text                  TEXT            NOT NULL DEFAULT '',
    post_format                post_format     NOT NULL DEFAULT '',

    -- Этап 2: медиа (фото/видео) — JSON-массив объектов {token, type}
    media_tokens               JSONB           NOT NULL DEFAULT '[]'::jsonb,

    -- Этап 4: текст кнопки участия (по умолчанию "участвовать")
    button_text                TEXT            NOT NULL DEFAULT 'участвовать',

    -- Этап 5: каналы, на которые надо подписаться (id из channels)
    subscribe_channel_ids      BIGINT[]        NOT NULL DEFAULT '{}',

    -- Этап 6: каналы, где публикуется розыгрыш
    publish_channel_ids        BIGINT[]        NOT NULL DEFAULT '{}',

    -- Этап 10.5: где публиковать итоги
    results_in_original_post   BOOLEAN         NOT NULL DEFAULT FALSE,
    results_post_text          TEXT            NOT NULL DEFAULT '',

    -- Этапы 7-8: даты публикации/итогов (по МСК)
    publish_at                 TIMESTAMPTZ,
    finish_at                  TIMESTAMPTZ,

    -- Этап 9: кол-во победителей (1-100)
    winners_count              INT             NOT NULL DEFAULT 1 CHECK (winners_count BETWEEN 1 AND 100),

    -- Этап 10: сообщение победителям (опц.)
    winners_message            TEXT            NOT NULL DEFAULT '',

    -- Этап 11: реферальная система (опц.). Лимит приглашений по умолчанию 10.
    referral_enabled           BOOLEAN         NOT NULL DEFAULT FALSE,
    referral_limit             INT             NOT NULL DEFAULT 10 CHECK (referral_limit >= 0),

    -- Этап 12: капча (опц.). captcha_percent — доля участников при captcha_mode='percent'
    captcha_mode               captcha_mode    NOT NULL DEFAULT 'none',
    captcha_percent            INT             NOT NULL DEFAULT 0,

    -- Этап 13: продвижение (опц., бесплатно для создателя)
    promotion_enabled          BOOLEAN         NOT NULL DEFAULT FALSE,

    -- FSM: текущий шаг создания черновика (NULL = не в режиме создания)
    draft_step                 TEXT,

    status                     giveaway_status NOT NULL DEFAULT 'draft',

    -- Ссылки на опубликованные сообщения (для редактирования/удаления)
    publish_message_ids        JSONB           NOT NULL DEFAULT '{}'::jsonb, -- {channel_chat_id: message_mid}
    results_message_ids        JSONB           NOT NULL DEFAULT '{}'::jsonb,
    winners_user_ids           BIGINT[]        NOT NULL DEFAULT '{}',

    created_at                 TIMESTAMPTZ     NOT NULL DEFAULT now(),
    updated_at                 TIMESTAMPTZ     NOT NULL DEFAULT now()
);

CREATE INDEX idx_giveaways_owner_status ON giveaways (owner_user_id, status);
CREATE INDEX idx_giveaways_status_publish ON giveaways (status, publish_at) WHERE status = 'scheduled';
CREATE INDEX idx_giveaways_status_finish ON giveaways (status, finish_at) WHERE status = 'active';

-- ---------------------------------------------------------------------------
-- Участники розыгрышей.
-- ---------------------------------------------------------------------------
CREATE TABLE participants
(
    id             BIGSERIAL PRIMARY KEY,
    giveaway_id    BIGINT  NOT NULL REFERENCES giveaways (id) ON DELETE CASCADE,
    user_id        BIGINT  NOT NULL REFERENCES users (user_id) ON DELETE CASCADE,
    -- Этап 11: реферальная система
    referral_by    BIGINT  REFERENCES users (user_id) ON DELETE SET NULL,
    tickets        INT     NOT NULL DEFAULT 1,
    captcha_passed BOOLEAN NOT NULL DEFAULT TRUE,
    joined_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    UNIQUE (giveaway_id, user_id)
);

CREATE INDEX idx_participants_giveaway ON participants (giveaway_id);
CREATE INDEX idx_participants_user ON participants (user_id);

-- ---------------------------------------------------------------------------
-- Подробная информация об участии (отдельная БД по ТЗ):
-- на какие каналы подписался, через сколько отписался (NULL = не отписался).
-- ---------------------------------------------------------------------------
CREATE TABLE subscriptions_log
(
    id                 BIGSERIAL PRIMARY KEY,
    participant_id     BIGINT      REFERENCES participants (id) ON DELETE CASCADE,
    giveaway_id        BIGINT      REFERENCES giveaways (id) ON DELETE CASCADE,
    channel_id         BIGINT      REFERENCES channels (id) ON DELETE CASCADE,
    user_id            BIGINT      NOT NULL,
    subscribed_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    unsubscribed_at    TIMESTAMPTZ,                -- NULL = "nan" (не отписался)
    sub_source         TEXT        NOT NULL DEFAULT 'post' -- post / utm
);

CREATE INDEX idx_sublog_user ON subscriptions_log (user_id);
CREATE INDEX idx_sublog_channel ON subscriptions_log (channel_id);

-- ---------------------------------------------------------------------------
-- Реферальные ссылки (по одной на участника-розыгрыша).
-- ---------------------------------------------------------------------------
CREATE TABLE referral_links
(
    giveaway_id BIGINT NOT NULL REFERENCES giveaways (id) ON DELETE CASCADE,
    user_id     BIGINT NOT NULL REFERENCES users (user_id) ON DELETE CASCADE,
    code        TEXT   NOT NULL UNIQUE,
    created_at  TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (giveaway_id, user_id)
);

-- ---------------------------------------------------------------------------
-- UTM-ссылки на каналы, где бот админ — для отслеживания источников подписок.
-- Формат диплинка: https://max.ru/<botName>?start=utm_<code>
-- ---------------------------------------------------------------------------
CREATE TABLE utm_links
(
    id         BIGSERIAL PRIMARY KEY,
    channel_id BIGINT REFERENCES channels (id) ON DELETE SET NULL,
    source     TEXT   NOT NULL DEFAULT '',
    campaign   TEXT   NOT NULL DEFAULT '',
    code       TEXT   NOT NULL UNIQUE,
    clicks     BIGINT NOT NULL DEFAULT 0,
    signups    BIGINT NOT NULL DEFAULT 0,
    created_at TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE INDEX idx_utm_channel ON utm_links (channel_id);

-- ---------------------------------------------------------------------------
-- Автообновление updated_at
-- ---------------------------------------------------------------------------
CREATE OR REPLACE FUNCTION touch_updated_at()
    RETURNS TRIGGER AS
$$
BEGIN
    NEW.updated_at = now();
    RETURN NEW;
END;
$$ LANGUAGE plpgsql;

CREATE TRIGGER trg_users_touch BEFORE UPDATE ON users
    FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
CREATE TRIGGER trg_channels_touch BEFORE UPDATE ON channels
    FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
CREATE TRIGGER trg_giveaways_touch BEFORE UPDATE ON giveaways
    FOR EACH ROW EXECUTE FUNCTION touch_updated_at();
