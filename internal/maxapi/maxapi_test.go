package maxapi

import (
	"encoding/json"
	"testing"

	"github.com/max-messenger/max-bot-api-client-go/v2/model"
	"github.com/stretchr/testify/assert"
	"github.com/stretchr/testify/require"
)

// TestReplyKeyboardBodyJSON — формат reply-keyboard: кнопки ОБЯЗАНЫ лежать на
// верхнем уровне attachment (асимметрия MAX API: у inline — в payload.buttons,
// у reply_keyboard — сверху). Живой запрос с payload.buttons MAX отвергает:
// 400 "Field 'buttons' cannot be null".
func TestReplyKeyboardBodyJSON(t *testing.T) {
	notify := false
	body := replyKeyboardBody{
		Text:   "Меню",
		Notify: &notify,
		Attachments: []replyKeyboardAttachment{{
			Type: string(model.AttachReplyKeyboard),
			Buttons: [][]*model.Button{
				{{Text: "🎁 Создать розыгрыш", Type: model.ButtonMessage, Payload: "create"}},
				{{Text: "📋 Мои розыгрыши", Type: model.ButtonMessage, Payload: "my_giveaways"}},
			},
		}},
	}
	raw, err := json.Marshal(body)
	require.NoError(t, err)

	var probe struct {
		Attachments []struct {
			Type    string `json:"type"`
			Buttons  [][]struct {
				Text    string `json:"text"`
				Type    string `json:"type"`
				Payload string `json:"payload"`
			} `json:"buttons"`
			Payload *json.RawMessage `json:"payload"`
		} `json:"attachments"`
	}
	require.NoError(t, json.Unmarshal(raw, &probe))
	require.Len(t, probe.Attachments, 1)

	at := probe.Attachments[0]
	assert.Equal(t, "reply_keyboard", at.Type)
	assert.Nil(t, at.Payload, "payload быть не должно — кнопки на верхнем уровне")
	require.Len(t, at.Buttons, 2)
	assert.Equal(t, "🎁 Создать розыгрыш", at.Buttons[0][0].Text)
	assert.Equal(t, "message", at.Buttons[0][0].Type, "reply-кнопка = SendMessageButton (type=message)")
	assert.Equal(t, "create", at.Buttons[0][0].Payload)

	// Страховка от регрессии: строка payload.buttons не должна встречаться.
	assert.NotContains(t, string(raw), `"payload":{"buttons"`)
}

// TestParseDataPayload — извлечение payload reply-кнопки из raw webhook.
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
