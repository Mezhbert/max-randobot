package webapp

import (
	"testing"

	"github.com/maxbot/giveawaybot/internal/store"
	"github.com/stretchr/testify/assert"
)

// TestRedirectTarget — выбор цели /r/<code>: пост только на max.ru
// (open redirect на произвольный домен запрещён), канал, фолбэк.
func TestRedirectTarget(t *testing.T) {
	// Пост-источник на max.ru — ведём на пост.
	l := store.UTMLink{PostRef: "https://max.ru/chan/42"}
	assert.Equal(t, "https://max.ru/chan/42", redirectTarget(nil, nil, l))

	// Пост на чужом домене — НЕ ведём туда (open redirect), без БД → фолбэк.
	l = store.UTMLink{PostRef: "https://evil.example.com/phish"}
	assert.Equal(t, "/app/index.html", redirectTarget(nil, nil, l))

	// Ничего не задано — фолбэк на главный экран.
	assert.Equal(t, "/app/index.html", redirectTarget(nil, nil, store.UTMLink{}))
}
