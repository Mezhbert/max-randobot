-- 0011: реферальная система — фиксация приглашений (кто кого пригласил
-- в конкретный розыгрыш) для кредита «+1 билет за друга».
--
-- Связка: инвайтер имеет строку в referral_links (giveaway_id, user_id, code);
-- приглашённый кликает deep-link max.ru/<bot>/start/ref_<code> → bot_started
-- с payload → здесь фиксируется пара (розыгрыш, инвайтер, приглашённый).
-- Кредит выдаётся в момент, когда приглашённый становится участником.

CREATE TABLE IF NOT EXISTS referral_invites
(
    giveaway_id     BIGINT NOT NULL,
    inviter_user_id BIGINT NOT NULL,
    invitee_user_id BIGINT NOT NULL REFERENCES users (user_id) ON DELETE CASCADE,
    created_at      TIMESTAMPTZ NOT NULL DEFAULT now(),
    PRIMARY KEY (giveaway_id, invitee_user_id),
    FOREIGN KEY (giveaway_id, inviter_user_id)
        REFERENCES referral_links (giveaway_id, user_id) ON DELETE CASCADE
);

CREATE INDEX IF NOT EXISTS idx_ref_invites_inviter
    ON referral_invites (giveaway_id, inviter_user_id);
