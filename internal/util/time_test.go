package util

import (
	"testing"
	"time"

	"github.com/stretchr/testify/assert"
)

func TestParseMoscowTime_Valid(t *testing.T) {
	t1, err := ParseMoscowTime("31.12.2026 18:00")
	assert.NoError(t, err)
	// Хранится в UTC: 18:00 МСК = 15:00 UTC.
	assert.Equal(t, "2026-12-31 15:00:00 +0000 UTC", t1.UTC().String())
}

func TestParseMoscowTime_Invalid(t *testing.T) {
	_, err := ParseMoscowTime("not-a-date")
	assert.Error(t, err)

	_, err = ParseMoscowTime("2026-12-31 18:00") // неверный формат (ISO вместо ДД.ММ.ГГГГ)
	assert.Error(t, err)
}

func TestFormatMoscowTime_Roundtrip(t *testing.T) {
	orig := "15.03.2026 09:30"
	parsed, err := ParseMoscowTime(orig)
	assert.NoError(t, err)
	assert.Equal(t, orig, FormatMoscowTime(parsed))
}

func TestFormatMoscowTime_ZeroIsDash(t *testing.T) {
	assert.Equal(t, "—", FormatMoscowTime(time.Time{}))
}

func TestMoscowLocation_NotNil(t *testing.T) {
	loc := MoscowLocation()
	assert.NotNil(t, loc)
	// Москва = UTC+3.
	_, off := time.Date(2026, 1, 1, 0, 0, 0, 0, loc).Zone()
	assert.Equal(t, 3*3600, off)
}

// TestParseMoscowTime_TimeOnly_FutureToday — короткий формат ЧЧ:ММ, время в будущем сегодня.
func TestParseMoscowTime_TimeOnly_FutureToday(t *testing.T) {
	loc := MoscowLocation()
	now := time.Now().In(loc)
	// Вводим «через 2 часа» — точно сегодня.
	futureHour := now.Add(2 * time.Hour)
	input := futureHour.Format("15:04")
	t1, err := ParseMoscowTime(input)
	assert.NoError(t, err)
	// Должно быть в будущем.
	assert.True(t, t1.After(now.Add(-time.Hour)),
		"короткий формат должен дать будущее время: input=%s got=%s now=%s", input, t1, now)
}

// TestParseMoscowTime_TimeOnly_PastTodayRollsToTomorrow — если время сегодня прошло → завтра.
func TestParseMoscowTime_TimeOnly_PastTodayRollsToTomorrow(t *testing.T) {
	loc := MoscowLocation()
	now := time.Now().In(loc)
	// Вводим «2 часа назад» — должно перекинуть на завтра.
	pastHour := now.Add(-2 * time.Hour)
	input := pastHour.Format("15:04")
	t1, err := ParseMoscowTime(input)
	assert.NoError(t, err)
	// Результат должен быть в будущем (завтра).
	assert.True(t, t1.After(now), "прошедшее время сегодня → завтра: input=%s got=%s now=%s", input, t1, now)
}
