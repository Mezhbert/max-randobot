package util

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
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
	CBAnalytics        = "analytics"    // UTM-аналитика (главное меню)
	CBAnalyticsTab     = "an_tab:"      // + channels|giveaways (вкладки аналитики)

	// Фильтры "мои розыгрыши"
	CBGiveawayFilter = "gw_filter:" // + status
	CBGwList         = "gw_list"
	CBGwOpen         = "gw_open:" // + giveaway_id
	CBGwCancel       = "gw_cancel:"
	CBGwDelete       = "gw_delete:"
	CBGwEdit         = "gw_edit:"                    // продолжить создание/редактировать
	CBGwEditField    = "gw_ef:"                      // + field:giveaway_id (меню редактирования поля)
	CBGwEditSet      = "gw_es:"                      // + key:value:giveaway_id (выбор-опции)
	CBGwExtraWinner  = "gw_extra_winner:"            // + giveaway_id (доп. победитель)
	CBGwAnalytics    = "gw_analytics:"               // + giveaway_id (аналитика розыгрыша)

	// Каналы
	CBChannelAdd       = "ch_add"
	CBChannelBack      = "ch_back"
	CBChannelAnalytics = "ch_analytics" // вход в аналитику/UTM канала
	CBChannelRefresh   = "ch_refresh"   // 🔄 обновить статус админства бота во всех каналах

	// UTM-трекинг
	CBChannelUTM  = "ch_utm:"  // + channel_id — меню UTM конкретного канала
	CBUTMCreate   = "utm_new:" // + channel_id — начать создание UTM-ссылки
	CBUTMStats    = "utm_st:"  // + channel_id — показать статистику UTM
	CBUTMExport   = "utm_exp:" // + channel_id — экспорт CSV

	// FSM создания
	CBFSMBack     = "fsm_back"
	CBFSMCancel   = "fsm_cancel"
	CBFSMSkip     = "fsm_skip"
	CBFSMOption   = "fsm_opt:"
	CBFSMConfirm  = "fsm_confirm"
	CBFSMReject   = "fsm_reject"
	CBFSMSaveDraft = "fsm_save" // сохранить в черновик (выйти из FSM, сохранив)
	CBFSMHint     = "fsm_hint"  // 💡 краткая подсказка по текущему шагу

	// Кнопка "Участвовать" под постом розыгрыша
	CBParticipate = "part:" // + giveaway_id
)

// MainMenuKeyboard — клавиатура главного меню. Каждая кнопка на отдельной строке.
func MainMenuKeyboard() *model.Keyboard {
	kb := model.NewKeyboard()
	kb.AddRow().AddCallBack("🎁 Создать розыгрыш", CBCreateGiveaway)
	kb.AddRow().AddCallBack("📋 Мои розыгрыши", CBMyGiveaways)
	kb.AddRow().AddCallBack("📢 Мои каналы", CBMyChannels)
	kb.AddRow().AddCallBack("🎟 Мои участия", CBMyParticipations)
	kb.AddRow().AddCallBack("📊 Аналитика", CBAnalytics)
	kb.AddRow().AddCallBack("🆘 Поддержка", CBSupport)
	return kb
}

// NavKeyboard — 💡 / Назад / Отменить.
func NavKeyboard() *model.Keyboard {
	kb := model.NewKeyboard()
	kb.AddRow().
		AddCallBack("💡", CBFSMHint).
		AddCallBack("← Назад", CBFSMBack).
		AddCallBack("✖ Отменить", CBFSMCancel)
	return kb
}

// NavWithSkipKeyboard — Пропустить / 💡 / Назад / Отменить.
func NavWithSkipKeyboard() *model.Keyboard {
	kb := model.NewKeyboard()
	kb.AddRow().AddCallBack("⏭ Пропустить", CBFSMSkip)
	kb.AddRow().
		AddCallBack("💡", CBFSMHint).
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

// ParticipateKeyboardBuilder — кнопка «Участвовать (N)» под постом розыгрыша.
// Использует ButtonLink с deep link (https://max.ru/<bot>/start/part_<id>) —
// при нажатии MAX ОТКРЫВАЕТ диалог с ботом (callback-кнопка этого не делает).
// botUsername — публичное имя бота (без @). participants — текущее число
// участников (отображается в кнопке).
func ParticipateKeyboardBuilder(buttonText string, giveawayID int64, botUsername string, participants int) *model.Keyboard {
	kb := model.NewKeyboard()
	botName := botUsername
	if botName == "" {
		botName = "max_bot"
	}
	link := "https://max.ru/" + botName + "/start/part_" + strconv.FormatInt(giveawayID, 10)
	label := fmt.Sprintf("%s (%d)", buttonText, participants)
	kb.AddRow().AddLink(label, link)
	return kb
}

// PreviewKeyboardBuilder — кнопка участия для ПРЕДПРОСМОТРА (с пометкой "превью",
// визуально та же что будет в посте, но callback никуда не ведёт).
func PreviewKeyboardBuilder(buttonText string, giveawayID int64) *model.Keyboard {
	kb := model.NewKeyboard()
	// Текст кнопки как будет в посте, callback пустой (no-op).
	kb.AddRow().AddCallBack(buttonText+" (превью)", "preview_btn:"+strconv.FormatInt(giveawayID, 10))
	return kb
}

// GiveawayAttachments строит список вложений для поста розыгрыша:
// медиа (image/video по токенам) + inline-клавиатура кнопки участия.
// Используется и при публикации, и при предпросмотре (preview=true → PreviewKeyboard).
// botUsername — для построения deep-link кнопки участия (открывает диалог).
func GiveawayAttachments(buttonText string, giveawayID int64, mediaTokens []MediaTokenSimple, preview bool, botUsername string) []model.Attachment {
	var attachments []model.Attachment
	for _, m := range mediaTokens {
		switch m.Type {
		case "image":
			attachments = append(attachments, model.Attachment{
				Type:    model.AttachImage,
				Payload: model.Payload{Token: m.Token},
			})
		case "video":
			attachments = append(attachments, model.Attachment{
				Type:    model.AttachVideo,
				Payload: model.Payload{Token: m.Token},
			})
		}
	}
	// Клавиатура как inline-attachment через Build().
	var kb *model.Keyboard
	if preview {
		kb = PreviewKeyboardBuilder(buttonText, giveawayID)
	} else {
		kb = ParticipateKeyboardBuilder(buttonText, giveawayID, botUsername, 0)
	}
	attachments = append(attachments, kb.Build())
	return attachments
}

// MediaTokenSimple — упрощённая пара тип/токен (без зависимости на store).
type MediaTokenSimple struct {
	Type  string
	Token string
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

// CancelEditKeyboard — отмена редактирования поля, назад в меню розыгрыша.
func CancelEditKeyboard(giveawayID int64) *model.Keyboard {
	kb := model.NewKeyboard()
	kb.AddRow().AddCallBack("✖ Отмена", CBGwOpen+strconv.FormatInt(giveawayID, 10))
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
