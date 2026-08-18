package maxapi

import (
	"testing"

	"github.com/stretchr/testify/assert"
)

// TestParseDataPayload — извлечение payload data-attachment из raw webhook.
// Функция оставлена для будущего: reply-keyboard в MAX ботам сейчас недоступен
// (платформа выкидывает attachment при отправке), но формат входящего
// data-сообщения зафиксирован тестом.
func TestParseDataPayload(t *testing.T) {
	body := []byte(`{"update_type":"message_created","user_id":42,` +
		`"message":{"body":{"mid":"m1","text":"","attachments":[` +
		`{"type":"data","payload":{"data":"my_giveaways"}}]}}}`)
	uid, payload := ParseDataPayload(body)
	assert.Equal(t, int64(42), uid)
	assert.Equal(t, "my_giveaways", payload)

	// Не message_created — игнор.
	uid, payload = ParseDataPayload([]byte(`{"update_type":"bot_started","payload":"part_1"}`))
	assert.Equal(t, int64(0), uid)
	assert.Equal(t, "", payload)

	// Без data-attachment — пусто.
	uid, payload = ParseDataPayload([]byte(`{"update_type":"message_created","user_id":7,"message":{"body":{"text":"привет"}}}`))
	assert.Equal(t, int64(0), uid)
	assert.Equal(t, "", payload)

	// Битый JSON — не паникует.
	assert.NotPanics(t, func() { ParseDataPayload([]byte("not json")) })
}

// TestParseStartedPayload — payload диплинка из bot_started (raw body).
func TestParseStartedPayload(t *testing.T) {
	assert.Equal(t, "utm_abc", ParseStartedPayload(
		[]byte(`{"update_type":"bot_started","payload":"utm_abc"}`)))
	assert.Equal(t, "", ParseStartedPayload(
		[]byte(`{"update_type":"message_created","payload":"x"}`)))
	assert.Equal(t, "", ParseStartedPayload([]byte(`bad json`)))
}

// TestParseStartedUserID — user_id из bot_started (raw body).
func TestParseStartedUserID(t *testing.T) {
	assert.Equal(t, int64(123), ParseStartedUserID(
		[]byte(`{"update_type":"bot_started","user":{"user_id":123}}`)))
	assert.Equal(t, int64(0), ParseStartedUserID([]byte(`{}`)))
}
