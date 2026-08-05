package draw

import (
	"math/rand"
	"testing"

	"github.com/stretchr/testify/assert"
)

func TestWeighted_EmptyAndZeroN(t *testing.T) {
	assert.Nil(t, Weighted(nil, 3, nil))
	assert.Nil(t, Weighted([]Entry{{UserID: 1, Tickets: 5}}, 0, nil))
}

func TestWeighted_NMoreThanEntries(t *testing.T) {
	// Участников меньше, чем нужно победителей — возвращаются все.
	entries := []Entry{{UserID: 1, Tickets: 1}, {UserID: 2, Tickets: 1}}
	got := Weighted(entries, 5, rand.New(rand.NewSource(1)))
	assert.ElementsMatch(t, []int64{1, 2}, got)
}

func TestWeighted_AllWinnersDistinct(t *testing.T) {
	// Выбор без возвращения: нет повторов.
	entries := []Entry{
		{UserID: 1, Tickets: 3}, {UserID: 2, Tickets: 3}, {UserID: 3, Tickets: 3},
		{UserID: 4, Tickets: 3}, {UserID: 5, Tickets: 3},
	}
	got := Weighted(entries, 3, rand.New(rand.NewSource(42)))
	assert.Len(t, got, 3)
	seen := map[int64]bool{}
	for _, id := range got {
		assert.False(t, seen[id], "duplicate winner %d", id)
		seen[id] = true
	}
}

func TestWeighted_HigherTicketsHigherChance(t *testing.T) {
	// Участник с 1000 билетов должен выигрывать почти всегда против 1 билета.
	heavy := int64(1)
	light := int64(2)
	heavyWins, lightWins := 0, 0
	const trials = 500
	for i := 0; i < trials; i++ {
		entries := []Entry{{UserID: heavy, Tickets: 1000}, {UserID: light, Tickets: 1}}
		got := Weighted(entries, 1, rand.New(rand.NewSource(int64(i))))
		if got[0] == heavy {
			heavyWins++
		} else {
			lightWins++
		}
	}
	assert.Greater(t, heavyWins, lightWins*20, "тяжёлый участник должен выигрывать подавляюще чаще: heavy=%d light=%d", heavyWins, lightWins)
}

func TestWeighted_WeightDistribution(t *testing.T) {
	// 3 участника с весами 1:2:3 → ожидаемые доли ~16.7%/33.3%/50%.
	entries := []Entry{{UserID: 10, Tickets: 1}, {UserID: 20, Tickets: 2}, {UserID: 30, Tickets: 3}}
	counts := map[int64]int{}
	const trials = 6000
	r := rand.New(rand.NewSource(7))
	for i := 0; i < trials; i++ {
		got := Weighted(entries, 1, r)
		counts[got[0]]++
	}
	// Допускаем ±5% отклонение.
	assert.InDelta(t, 1000, counts[10], 300)
	assert.InDelta(t, 2000, counts[20], 300)
	assert.InDelta(t, 3000, counts[30], 300)
}

func TestWeighted_ZeroTicketsTreatedAsOne(t *testing.T) {
	// Битые данные (tickets=0) не выкидывают участника — дают минимальный шанс.
	entries := []Entry{{UserID: 1, Tickets: 0}, {UserID: 2, Tickets: 0}}
	got := Weighted(entries, 2, rand.New(rand.NewSource(1)))
	assert.ElementsMatch(t, []int64{1, 2}, got)
}

func TestWeighted_DeterministicWithSeed(t *testing.T) {
	entries := []Entry{
		{UserID: 1, Tickets: 1}, {UserID: 2, Tickets: 1}, {UserID: 3, Tickets: 1},
	}
	a := Weighted(entries, 2, rand.New(rand.NewSource(99)))
	b := Weighted(entries, 2, rand.New(rand.NewSource(99)))
	assert.Equal(t, a, b)
}
