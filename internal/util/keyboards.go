package util

import (
	"crypto/rand"
	"encoding/hex"
	"strconv"

	"github.com/max-messenger/max-bot-api-client-go/v2/model"
)

// --- callback payload'ы (строки, которые вернутся в update.Callback.Payload) ---

// Главное меню
const (
	CBMainMenu         = "menu"
	CBCreateGiveaway   = "create"
	CBMyGiveaways      = "my_giveaways"
	CBMyChannels       = "my_channels"
	CBMyParticipations = "my_participations"
	CBSupport          = "support"

	// Фильтры "мои розыгрыши"
	CBGiveawayFilter = "gw_filter:" // + status
	CBGwList         = "gw_list"
	CBGwOpen         = "gw_open:" // + giveaway_id
	CBGwCancel       = "gw_cancel:"
	CBGwDelete       = "gw_delete:"
	CBGwEdit         = "gw_edit:"

	// Каналы
	CBChannelAdd       = "ch_add"
	CBChannelBack      = "ch_back"
	CBChannelAnalytics = "ch_analytics" // вход в аналитику/UTM канала

	// UTM-трекинг
	CBChannelUTM  = "ch_utm:"  // + channel_id — меню UTM конкретного канала
	CBUTMCreate   = "utm_new:" // + channel_id — начать создание UTM-ссылки
	CBUTMStats    = "utm_st:"  // + channel_id — показать статистику UTM
	CBUTMExport   = "utm_exp:" // + channel_id — экспорт CSV

	// FSM создания
	CBFSMBack    = "fsm_back"
	CBFSMCancel  = "fsm_cancel"
	CBFSMSkip    = "fsm_skip"
	CBFSMOption  = "fsm_opt:"
	CBFSMConfirm = "fsm_confirm"
	CBFSMReject  = "fsm_reject"

	// Кнопка "Участвовать" под постом розыгрыша
	CBParticipate = "part:" // + giveaway_id
)

// MainMenuKeyboard — клавиатура главного меню.
func MainMenuKeyboard() *model.Keyboard {
	kb := model.NewKeyboard()
	kb.AddRow().AddCallBack("🎁 Создать розыгрыш", CBCreateGiveaway)
	kb.AddRow().
		AddCallBack("📋 Мои розыгрыши", CBMyGiveaways).
		AddCallBack("📢 Мои каналы", CBMyChannels)
	kb.AddRow().
		AddCallBack("🎟 Мои участия", CBMyParticipations).
		AddCallBack("🆘 Поддержка", CBSupport)
	return kb
}

// NavKeyboard — Назад / Отменить.
func NavKeyboard() *model.Keyboard {
	kb := model.NewKeyboard()
	kb.AddRow().
		AddCallBack("← Назад", CBFSMBack).
		AddCallBack("✖ Отменить", CBFSMCancel)
	return kb
}

// NavWithSkipKeyboard — то же + «Пропустить».
func NavWithSkipKeyboard() *model.Keyboard {
	kb := model.NewKeyboard()
	kb.AddRow().AddCallBack("⏭ Пропустить", CBFSMSkip)
	kb.AddRow().
		AddCallBack("← Назад", CBFSMBack).
		AddCallBack("✖ Отменить", CBFSMCancel)
	return kb
}

// CancelOnlyKeyboard — только кнопка отмены.
func CancelOnlyKeyboard() *model.Keyboard {
	kb := model.NewKeyboard()
	kb.AddRow().AddCallBack("✖ Отменить создание", CBFSMCancel)
	return kb
}

// ConfirmKeyboard — Принять/Отклонить (предпросмотр).
func ConfirmKeyboard() *model.Keyboard {
	kb := model.NewKeyboard()
	kb.AddRow().
		AddCallBack("✅ Принять", CBFSMConfirm).
		AddCallBack("❌ Отклонить", CBFSMReject)
	kb.AddRow().AddCallBack("← Назад", CBFSMBack)
	return kb
}

// ParticipateKeyboardBuilder — возвращает *Keyboard для кнопки под постом розыгрыша.
func ParticipateKeyboardBuilder(buttonText string, giveawayID int64) *model.Keyboard {
	kb := model.NewKeyboard()
	kb.AddRow().AddCallBack(buttonText, CBParticipate+strconv.FormatInt(giveawayID, 10))
	return kb
}

// BackKeyboard — «В меню».
func BackKeyboard() *model.Keyboard {
	kb := model.NewKeyboard()
	kb.AddRow().AddCallBack("🏠 В меню", CBMainMenu)
	return kb
}

// ChannelAddKeyboard — клавиатура режима добавления канала (отмена).
func ChannelAddKeyboard() *model.Keyboard {
	kb := model.NewKeyboard()
	kb.AddRow().AddCallBack("✖ Отмена", CBChannelBack)
	return kb
}

// CancelKeyboard — кнопка отмены произвольного режима.
func CancelKeyboard() *model.Keyboard {
	kb := model.NewKeyboard()
	kb.AddRow().AddCallBack("✖ Отмена", CBMainMenu)
	return kb
}

// ChannelUTMKeyboard — меню канала: UTM-операции.
func ChannelUTMKeyboard(channelID int64) *model.Keyboard {
	id := strconv.FormatInt(channelID, 10)
	kb := model.NewKeyboard()
	kb.AddRow().AddCallBack("🔗 Создать UTM-ссылку", CBUTMCreate+id)
	kb.AddRow().AddCallBack("📊 Аналитика UTM", CBUTMStats+id)
	kb.AddRow().AddCallBack("🏠 В меню", CBMainMenu)
	return kb
}

// ChannelUTMStatsKeyboard — клавиатура на экране статистики (экспорт + назад).
func ChannelUTMStatsKeyboard(channelID int64) *model.Keyboard {
	id := strconv.FormatInt(channelID, 10)
	kb := model.NewKeyboard()
	kb.AddRow().AddCallBack("📎 Экспорт CSV", CBUTMExport+id)
	kb.AddRow().AddCallBack("🔗 Создать UTM", CBUTMCreate+id)
	kb.AddRow().AddCallBack("← К каналу", CBChannelUTM+id)
	kb.AddRow().AddCallBack("🏠 В меню", CBMainMenu)
	return kb
}

// RandomCode генерирует случайную строку заданной длины (hex).
func RandomCode(n int) string {
	b := make([]byte, n/2+1)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)[:n]
}
