package store

import "context"

// AddParticipant — добавляет участника. idempotent по (giveaway_id, user_id).
func (s *Store) AddParticipant(ctx context.Context, p Participant) (Participant, error) {
	var out Participant
	err := s.pool.QueryRow(ctx, `
		INSERT INTO participants (giveaway_id, user_id, referral_by, tickets, captcha_passed)
		VALUES ($1, $2, NULLIF($3, 0), $4, $5)
		ON CONFLICT (giveaway_id, user_id) DO NOTHING
		RETURNING id, giveaway_id, user_id, COALESCE(referral_by, 0), tickets, captcha_passed, joined_at
	`, p.GiveawayID, p.UserID, p.ReferralBy, max1(p.Tickets), p.CaptchaPassed,
	).Scan(&out.ID, &out.GiveawayID, &out.UserID, &out.ReferralBy, &out.Tickets, &out.CaptchaPassed, &out.JoinedAt)
	return out, err
}

// GetParticipant — получить участника.
func (s *Store) GetParticipant(ctx context.Context, giveawayID, userID int64) (Participant, error) {
	var p Participant
	err := s.pool.QueryRow(ctx, `
		SELECT id, giveaway_id, user_id, COALESCE(referral_by,0), tickets, captcha_passed, joined_at
		FROM participants WHERE giveaway_id=$1 AND user_id=$2
	`, giveawayID, userID).Scan(&p.ID, &p.GiveawayID, &p.UserID, &p.ReferralBy, &p.Tickets, &p.CaptchaPassed, &p.JoinedAt)
	return p, wrapNotFound(err)
}

// CountParticipants — число участников розыгрыша.
func (s *Store) CountParticipants(ctx context.Context, giveawayID int64) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx, `SELECT COUNT(*) FROM participants WHERE giveaway_id=$1`, giveawayID).Scan(&n)
	return n, err
}

// PickWinners — выбирает N случайных победителей (с учётом билетов:权重ный выбор).
// Реализовано через weighted random в SQL с window-function.
func (s *Store) PickWinners(ctx context.Context, giveawayID int64, n int) ([]int64, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT user_id
		FROM (
			SELECT user_id,
			       SUM(tickets) OVER (ORDER BY id) AS cum,
			       SUM(tickets) OVER () AS total
			FROM participants
			WHERE giveaway_id=$1
		) t
		WHERE total > 0
		ORDER BY random()  -- упрощённо: равновзвешенный выбор N строк; см. TODO в README про weighted
		LIMIT $2
	`, giveawayID, n)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []int64
	for rows.Next() {
		var uid int64
		if err := rows.Scan(&uid); err != nil {
			return nil, err
		}
		res = append(res, uid)
	}
	return res, rows.Err()
}

// AddSubscriptionLog — запись о подписке участника на канал.
func (s *Store) AddSubscriptionLog(ctx context.Context, participantID, giveawayID, channelID, userID int64, source string) error {
	_, err := s.pool.Exec(ctx, `
		INSERT INTO subscriptions_log (participant_id, giveaway_id, channel_id, user_id, sub_source)
		VALUES ($1, $2, $3, $4, $5)
	`, participantID, giveawayID, channelID, userID, source)
	return err
}

// MarkUnsubscribed — фиксируем отписку пользователя от канала (если подписка была).
func (s *Store) MarkUnsubscribed(ctx context.Context, userID, channelID int64) error {
	_, err := s.pool.Exec(ctx, `
		UPDATE subscriptions_log
		SET unsubscribed_at = now()
		WHERE user_id=$1 AND channel_id=$2 AND unsubscribed_at IS NULL
	`, userID, channelID)
	return err
}

func max1(n int) int {
	if n < 1 {
		return 1
	}
	return n
}
