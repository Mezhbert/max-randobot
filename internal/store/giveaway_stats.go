package store

import (
	"context"
	"time"
)

// GiveawayStats — статистика розыгрыша для аналитики.
type GiveawayStats struct {
	ParticipantsTotal int            // всего участников
	JoinsByDay        map[string]int // «02.01" → кол-во присоединившихся
	JoinsByHour       map[int]int    // час (0-23) → кол-во (для всплесков)
	NoUsername        int            // участники без username (возможные боты)
	Bots              int            // участники-боты (is_bot)
	FreshAccounts     int            // аккаунты младше 3 дней на момент join
	Unsubscribed      int            // отписались от каналов после участия
	Bursts            []string       // найденные всплески (описания)
}

// GiveawayParticipantStats собирает статистику участников розыгрыша:
// динамика по дням, распределение по часам, эвристики подозрительного трафика,
// отток (отписки) и свежие аккаунты.
func (s *Store) GiveawayParticipantStats(ctx context.Context, giveawayID int64) (GiveawayStats, error) {
	st := GiveawayStats{
		JoinsByDay:  map[string]int{},
		JoinsByHour: map[int]int{},
	}

	// Основная выборка: время присоединения, username, свежесть аккаунта.
	rows, err := s.pool.Query(ctx, `
		SELECT p.joined_at, COALESCE(u.username, ''), COALESCE(u.is_bot, false), u.created_at
		FROM participants p
		JOIN users u ON u.user_id = p.user_id
		WHERE p.giveaway_id = $1
		ORDER BY p.joined_at
	`, giveawayID)
	if err != nil {
		return st, err
	}
	defer rows.Close()

	for rows.Next() {
		var joinedAt time.Time
		var username string
		var isBot bool
		var createdAt time.Time
		if err := rows.Scan(&joinedAt, &username, &isBot, &createdAt); err != nil {
			return st, err
		}
		st.ParticipantsTotal++
		msk := joinedAt.In(MoscowLocationForStats())
		st.JoinsByDay[msk.Format("02.01")]++
		st.JoinsByHour[msk.Hour()]++
		if username == "" {
			st.NoUsername++
		}
		if isBot {
			st.Bots++
		}
		if !createdAt.IsZero() && joinedAt.Sub(createdAt) < 72*time.Hour {
			st.FreshAccounts++
		}
	}
	if err := rows.Err(); err != nil {
		return st, err
	}

	// Отток: участники, отписавшиеся от каналов розыгрыша (unsubscribed_at).
	if err := s.pool.QueryRow(ctx, `
		SELECT COUNT(DISTINCT sl.user_id)
		FROM subscriptions_log sl
		WHERE sl.giveaway_id = $1 AND sl.unsubscribed_at IS NOT NULL
	`, giveawayID).Scan(&st.Unsubscribed); err != nil {
		// Не критично для общей картины.
		st.Unsubscribed = 0
	}

	// Эвристика всплесков: >5 присоединений в один час.
	for hour, n := range st.JoinsByHour {
		if n >= 5 {
			st.Bursts = append(st.Bursts, time.Date(0, 0, 0, hour, 0, 0, 0, time.UTC).Format("15:04"))
		}
	}

	return st, nil
}

// MoscowLocationForStats — МСК для группировки статистики.
func MoscowLocationForStats() *time.Location {
	loc, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		return time.FixedZone("MSK", 3*3600)
	}
	return loc
}
