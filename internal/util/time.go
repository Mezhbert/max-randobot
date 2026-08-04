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

// ParseMoscowTime парсит дату в формате "ДД.ММ.ГГГГ ЧЧ:ММ" и интерпретирует
// её как время по МСК, возвращая UTC.
func ParseMoscowTime(s string) (time.Time, error) {
	t, err := time.ParseInLocation(DateTimeLayout, s, MoscowLocation())
	if err != nil {
		return time.Time{}, errors.New("дата должна быть в формате ДД.ММ.ГГГГ ЧЧ:ММ (например 31.12.2026 18:00)")
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
