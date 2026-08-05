// Package draw — weighted-выбор победителей розыгрыша.
//
// Раньше выбор делался в SQL через ORDER BY random() LIMIT N — это игнорировало
// билеты участников (каждый имел равный шанс независимо от tickets). Здесь —
// честный weighted random без возвращения: шанс участника пропорционален числу
// его билетов.
//
// Чистая функция над слайсом — детерминирована при фиксированном rand.Rand,
// что делает её тривиально тестируемой без БД.
package draw

import (
	"math/rand"
)

// Entry — участник с весом (числом билетов).
type Entry struct {
	UserID  int64
	Tickets int
}

// Weighted выбирает n победителей пропорционально билетам, без возвращения.
// Если участников меньше n — возвращаются все (в случайном порядке).
// Tickets<=0 трактуется как 1 (минимальный шанс), чтобы не выкидывать юзеров
// с битыми данными. r!=nil позволяет сделать выбор детерминированным в тестах;
// при r==nil используется новый локальный источник.
func Weighted(entries []Entry, n int, r *rand.Rand) []int64 {
	if n <= 0 || len(entries) == 0 {
		return nil
	}
	if r == nil {
		r = rand.New(rand.NewSource(rand.Int63()))
	}

	// Рабочая копия с нормализованными весами.
	pool := make([]Entry, len(entries))
	copy(pool, entries)
	for i := range pool {
		if pool[i].Tickets < 1 {
			pool[i].Tickets = 1
		}
	}

	limit := n
	if limit > len(pool) {
		limit = len(pool)
	}
	winners := make([]int64, 0, limit)

	for picked := 0; picked < limit && len(pool) > 0; picked++ {
		// Сумма весов оставшихся.
		total := 0
		for _, e := range pool {
			total += e.Tickets
		}
		// Цель в [0, total).
		target := r.Intn(total)
		cum := 0
		hit := 0
		for i, e := range pool {
			cum += e.Tickets
			if target < cum {
				hit = i
				break
			}
		}
		winners = append(winners, pool[hit].UserID)
		// Убираем выбранного — выбор без возвращения.
		pool = append(pool[:hit], pool[hit+1:]...)
	}
	return winners
}
