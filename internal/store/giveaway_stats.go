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
	Bursts            []string       // найденные всплески (описания)
}

// GiveawayParticipantStats собирает статистику участников розыгрыша:
// динамика по дням, распределение по часам, эвристики подозрительного трафика.
func (s *Store) GiveawayParticipantStats(ctx context.Context, giveawayID int64) (GiveawayStats, error) {
	st := GiveawayStats{
		JoinsByDay:  map[string]int{},
		JoinsByHour: map[int]int{},
	}

	// Основная выборка: время присоединения + наличие username.
	rows, err := s.pool.Query(ctx, `
		SELECT p.joined_at, COALESCE(u.username, '')
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
		if err := rows.Scan(&joinedAt, &username); err != nil {
			return st, err
		}
		st.ParticipantsTotal++
		msk := joinedAt.In(MoscowLocationForStats())
		st.JoinsByDay[msk.Format("02.01")]++
		st.JoinsByHour[msk.Hour()]++
		if username == "" {
			st.NoUsername++
		}
	}
	if err := rows.Err(); err != nil {
		return st, err
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
