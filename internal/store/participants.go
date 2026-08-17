package store

import (
	"context"
	"time"
)

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

// CountParticipant — участвует ли конкретный пользователь (0/1). Для проверки
// идемпотентности до капчи.
func (s *Store) CountParticipant(ctx context.Context, giveawayID, userID int64) (int, error) {
	var n int
	err := s.pool.QueryRow(ctx,
		`SELECT COUNT(*) FROM participants WHERE giveaway_id=$1 AND user_id=$2`,
		giveawayID, userID).Scan(&n)
	return n, err
}

// PickWinners оставлен для обратной совместимости, но weighted-выбор теперь
// выполняется в Go (см. internal/draw): SQL random() игнорировал билеты.
// Здесь лишь возвращает N случайных ID без учёта весов — не используйте напрямую.
func (s *Store) PickWinners(ctx context.Context, giveawayID int64, n int) ([]int64, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT user_id FROM participants WHERE giveaway_id=$1 ORDER BY random() LIMIT $2
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

// ParticipantTicket — участник с числом билетов (для weighted-выбора в Go).
type ParticipantTicket struct {
	UserID  int64
	Tickets int
}

// ListParticipantsForDraw возвращает участников розыгрыша с их билетами.
// Используется планировщиком для weighted-выбора победителей в Go
// (вместо SQL random(), который билеты игнорировал).
func (s *Store) ListParticipantsForDraw(ctx context.Context, giveawayID int64) ([]ParticipantTicket, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT user_id, tickets FROM participants WHERE giveaway_id=$1 ORDER BY id
	`, giveawayID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []ParticipantTicket
	for rows.Next() {
		var p ParticipantTicket
		if err := rows.Scan(&p.UserID, &p.Tickets); err != nil {
			return nil, err
		}
		res = append(res, p)
	}
	return res, rows.Err()
}

// CountParticipantsForDraw — число участников для розыгрыша (аналог CountParticipants,
// явное имя под сценарий finish).
func (s *Store) CountParticipantsForDraw(ctx context.Context, giveawayID int64) (int, error) {
	return s.CountParticipants(ctx, giveawayID)
}

// ParticipantRow — строка списка участников (для аналитики/удаления накруток).
type ParticipantRow struct {
	UserID    int64
	FirstName string
	Username  string
	IsBot     bool
	JoinedAt  time.Time
}

// ListParticipants — участники розыгрыша с данными юзера (для экрана участников).
func (s *Store) ListParticipants(ctx context.Context, giveawayID int64) ([]ParticipantRow, error) {
	rows, err := s.pool.Query(ctx, `
		SELECT p.user_id, COALESCE(u.first_name, ''), COALESCE(u.username, ''), COALESCE(u.is_bot, false), p.joined_at
		FROM participants p
		JOIN users u ON u.user_id = p.user_id
		WHERE p.giveaway_id = $1
		ORDER BY p.joined_at
	`, giveawayID)
	if err != nil {
		return nil, err
	}
	defer rows.Close()
	var res []ParticipantRow
	for rows.Next() {
		var r ParticipantRow
		if err := rows.Scan(&r.UserID, &r.FirstName, &r.Username, &r.IsBot, &r.JoinedAt); err != nil {
			return nil, err
		}
		res = append(res, r)
	}
	return res, rows.Err()
}

// DeleteParticipant удаляет участника из розыгрыша (анти-накрутка: боты,
// подозрительные).
func (s *Store) DeleteParticipant(ctx context.Context, giveawayID, userID int64) error {
	ct, err := s.pool.Exec(ctx,
		`DELETE FROM participants WHERE giveaway_id=$1 AND user_id=$2`, giveawayID, userID)
	if err != nil {
		return err
	}
	if ct.RowsAffected() == 0 {
		return ErrNotFound
	}
	return nil
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
