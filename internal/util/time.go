// Package util — вспомогательные функции: даты, клавиатуры, форматирование.
package util

import (
	"errors"
	"time"
)

// MoscowLocation — часовой пояс Москвы (бот живёт по МСК по ТЗ).
func MoscowLocation() *time.Location {
	loc, err := time.LoadLocation("Europe/Moscow")
	if err != nil {
		// fallback: UTC+3, если база таймзон недоступна.
		return time.FixedZone("MSK", 3*3600)
	}
	return loc
}

// DateTimeLayout — формат дат от пользователя по ТЗ: ДД.ММ.ГГГГ ЧЧ:ММ
const DateTimeLayout = "02.01.2006 15:04"

// TimeOnlyLayout — короткий формат: только время "ЧЧ:ММ" (без даты).
// При таком вводе дата подставляется автоматически (сегодня или завтра —
// чтобы время было в будущем).
const TimeOnlyLayout = "15:04"

// ParseMoscowTime парсит дату/время и интерпретирует как МСК, возвращая UTC.
// Поддерживает ДВА формата:
//   - полный: "ДД.ММ.ГГГГ ЧЧ:ММ" (например "31.12.2026 18:00")
//   - короткий: "ЧЧ:ММ" (например "18:00") — дата берётся сегодня; если это
//     время уже прошло сегодня, подставляется завтра. Удобно: юзер не ошибётся
//     с датой, набрав её вручную.
func ParseMoscowTime(s string) (time.Time, error) {
	loc := MoscowLocation()

	// Сначала пробуем короткий формат "ЧЧ:ММ".
	if t, err := time.ParseInLocation(TimeOnlyLayout, s, loc); err == nil {
		now := time.Now().In(loc)
		// Подставляем сегодняшнюю дату.
		t = time.Date(now.Year(), now.Month(), now.Day(), t.Hour(), t.Minute(), 0, 0, loc)
		// Если сегодня это время уже прошло — завтра.
		if !t.After(now) {
			t = t.AddDate(0, 0, 1)
		}
		return t.UTC(), nil
	}

	// Полный формат "ДД.ММ.ГГГГ ЧЧ:ММ".
	t, err := time.ParseInLocation(DateTimeLayout, s, loc)
	if err != nil {
		return time.Time{}, errors.New("введите время в формате ЧЧ:ММ (например 18:00) или ДД.ММ.ГГГГ ЧЧ:ММ")
	}
	return t.UTC(), nil
}

// FormatMoscowTime форматирует UTC-время в строку по МСК.
func FormatMoscowTime(t time.Time) string {
	if t.IsZero() {
		return "—"
	}
	return t.In(MoscowLocation()).Format(DateTimeLayout)
}
