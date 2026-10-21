package store

import "context"

// Реферальная система: «+1 билет за каждого приглашённого друга».
//
// Механика:
//  1. Участник при регистрации получает личную ссылку (EnsureReferralLink):
//     max.ru/<bot>/start/ref_<code> — код уникален для (розыгрыш, юзер).
//  2. Друг кликает → bot_started(payload=ref_<code>) → SaveReferralInvite
//     фиксирует пару (розыгрыш, инвайтер, приглашённый).
//  3. Приглашённый регистрируется в розыгрыше → ReferralCreditIfNeeded
//     добавляет инвайтеру +1 билет (в пределах лимита, по умолчанию 10).

// ReferralLink — личная ссылка участника (инвайтера).
type ReferralLink struct {
	GiveawayID int64
	UserID     int64 // инвайтер
	Code       string
}

// GetReferralLinkByCode — ссылка по коду диплинка (код без префикса ref_).
func (s *Store) GetReferralLinkByCode(ctx context.Context, code string) (ReferralLink, error) {
	var l ReferralLink
	err := s.pool.QueryRow(ctx, `
		SELECT giveaway_id, user_id, code FROM referral_links WHERE code=$1
	`, code).Scan(&l.GiveawayID, &l.UserID, &l.Code)
	return l, wrapNotFound(err)
}

// SaveReferralInvite — приглашённый кликнул ref-ссылку: фиксируем пару.
// Идемпотентно (повторный клик не дублирует). Самоприглашение игнорируем.
func (s *Store) SaveReferralInvite(ctx context.Context, giveawayID, inviterID, inviteeID int64) error {
	if inviterID == inviteeID {
		return nil
	}
	_, err := s.pool.Exec(ctx, `
		INSERT INTO referral_invites (giveaway_id, inviter_user_id, invitee_user_id)
		VALUES ($1, $2, $3)
		ON CONFLICT (giveaway_id, invitee_user_id) DO NOTHING
	`, giveawayID, inviterID, inviteeID)
	return err
}

// ReferralCreditIfNeeded — приглашённый inviteeID стал участником розыгрыша:
// если он пришёл по ref-ссылке и лимит инвайтера не исчерпан — +1 билет
// инвайтеру. Возвращает инвайтера, если кредит выдан.
//
// Вызывать ПОСЛЕ вставки участника-приглашённого: счётчик приглашённых
// считается по уже зарегистрировавшимся (включая текущего).
func (s *Store) ReferralCreditIfNeeded(ctx context.Context, giveawayID, inviteeID int64) (int64, bool, error) {
	var inviter int64
	err := s.pool.QueryRow(ctx, `
		SELECT inviter_user_id FROM referral_invites
		WHERE giveaway_id=$1 AND invitee_user_id=$2
	`, giveawayID, inviteeID).Scan(&inviter)
	if err != nil {
		return 0, false, nil // приглашён не по ссылке — органик
	}

	// Лимит приглашений инвайтера (0 → дефолт 10).
	var limit int
	if err := s.pool.QueryRow(ctx, `
		SELECT COALESCE(referral_limit, 10) FROM giveaways WHERE id=$1
	`, giveawayID).Scan(&limit); err != nil || limit <= 0 {
		limit = 10
	}

	// Сколько приглашённых уже зарегистрировались (включая текущего).
	var registered int
	if err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM referral_invites ri
		JOIN participants p ON p.giveaway_id = ri.giveaway_id AND p.user_id = ri.invitee_user_id
		WHERE ri.giveaway_id=$1 AND ri.inviter_user_id=$2
	`, giveawayID, inviter).Scan(&registered); err != nil {
		return 0, false, err
	}
	if registered > limit {
		return 0, false, nil // лимит исчерпан — текущий сверх лимита
	}

	tag, err := s.pool.Exec(ctx, `
		UPDATE participants SET tickets = tickets + 1
		WHERE giveaway_id=$1 AND user_id=$2
	`, giveawayID, inviter)
	if err != nil {
		return 0, false, err
	}
	if tag.RowsAffected() == 0 {
		return 0, false, nil // инвайтер сам не участник (покинул) — билет некуда
	}
	return inviter, true, nil
}

// ReferralInvitedCount — сколько приглашённых зарегистрировалось у юзера.
func (s *Store) ReferralInvitedCount(ctx context.Context, giveawayID, userID int64) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `
		SELECT COUNT(*) FROM referral_invites ri
		JOIN participants p ON p.giveaway_id = ri.giveaway_id AND p.user_id = ri.invitee_user_id
		WHERE ri.giveaway_id=$1 AND ri.inviter_user_id=$2
	`, giveawayID, userID).Scan(&n)
	return n, err
}
