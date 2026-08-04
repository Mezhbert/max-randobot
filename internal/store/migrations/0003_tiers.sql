-- ===========================================================================
-- 0003: Фундамент тарифов (платный/бесплатный функционал) — БЕЗ блокировок.
-- Закладываем схему и поля, middleware-хелперы добавятся в коде.
-- Потом легко прикрутить оплату и feature-гейты.
-- ===========================================================================

-- ---------------------------------------------------------------------------
-- owners — создатели розыгрышей (отдельно от обычных юзеров бота).
-- У создателя свой тариф, отличный от тарифа участника-юзера.
-- ---------------------------------------------------------------------------
CREATE TABLE owners
(
    user_id        BIGINT PRIMARY KEY REFERENCES users (user_id) ON DELETE CASCADE,
    tier           TEXT NOT NULL DEFAULT 'free',     -- 'free' | 'pro' | 'business'
    tier_expires_at TIMESTAMPTZ,                     -- NULL = бессрочно или free
    created_at     TIMESTAMPTZ NOT NULL DEFAULT now(),
    updated_at     TIMESTAMPTZ NOT NULL DEFAULT now()
);

CREATE TRIGGER trg_owners_touch BEFORE UPDATE ON owners
    FOR EACH ROW EXECUTE FUNCTION touch_updated_at();

-- ---------------------------------------------------------------------------
-- feature_limits — справочник лимитов по тарифам создателей.
-- feature_key: 'max_channels', 'referral', 'promotion', 'utm_links', ...
-- limit_value: -1 = безлимит, иначе число.
-- ---------------------------------------------------------------------------
CREATE TABLE feature_limits
(
    tier         TEXT NOT NULL,
    feature_key  TEXT NOT NULL,
    limit_value  INT  NOT NULL DEFAULT 0,
    updated_at   TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (tier, feature_key)
);

CREATE TRIGGER trg_feature_limits_touch BEFORE UPDATE ON feature_limits
    FOR EACH ROW EXECUTE FUNCTION touch_updated_at();

-- Значения по умолчанию (free — базовые ограничения, pro/business — безлимит).
INSERT INTO feature_limits (tier, feature_key, limit_value) VALUES
    ('free',     'max_channels',  5),
    ('free',     'utm_links',    10),
    ('free',     'referral',      0),   -- на free рефералка выключена (заготовка)
    ('free',     'promotion',     0),
    ('pro',      'max_channels', -1),
    ('pro',      'utm_links',    -1),
    ('pro',      'referral',     -1),
    ('pro',      'promotion',    -1),
    ('business', 'max_channels', -1),
    ('business', 'utm_links',    -1),
    ('business', 'referral',     -1),
    ('business', 'promotion',    -1)
ON CONFLICT (tier, feature_key) DO NOTHING;

-- ---------------------------------------------------------------------------
-- users: тариф участника-юзера (для буд. платной подписки бота).
-- ---------------------------------------------------------------------------
ALTER TABLE users
    ADD COLUMN IF NOT EXISTS tier           TEXT NOT NULL DEFAULT 'free',
    ADD COLUMN IF NOT EXISTS tier_expires_at TIMESTAMPTZ;

-- ---------------------------------------------------------------------------
-- giveaways: снапшот тарифа создателя на момент создания (историчность).
-- ---------------------------------------------------------------------------
ALTER TABLE giveaways
    ADD COLUMN IF NOT EXISTS owner_tier TEXT NOT NULL DEFAULT 'free';
