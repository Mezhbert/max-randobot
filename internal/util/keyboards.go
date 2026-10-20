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
	CBGwEditChTglSub = "gw_ets:"                     // + channel_id:giveaway_id (тоггл канала подписки в редактуре)
	CBGwEditChTglPub = "gw_etp:"                     // + channel_id:giveaway_id (тоггл канала публикации в редактуре)
	CBGwMediaDone    = "gw_media_done:"              // + giveaway_id (завершить ввод медиа)
	CBGwExtraWinner  = "gw_extra_winner:"            // + giveaway_id (доп. победитель)
	CBGwAnalytics    = "gw_analytics:"               // + giveaway_id (аналитика розыгрыша)
	CBGwParticipants = "gw_parts:"                   // + giveaway_id (список участников)
	CBGwKickPart     = "gw_kick:"                    // + giveaway_id:user_id (удалить участника)
	CBGwCSVParts     = "gw_csv:"                     // + giveaway_id (экспорт участников CSV)

	// UTM-трекинг (общее)
	CBUTMQuickCreate = "utm_quick"      // быстрая трекинг-ссылка без канала
	CBUTMSkipPost    = "utm_post_skip" // билдер ссылки: пропустить пост-источник

	// Каналы
	CBChannelAdd       = "ch_add"
	CBChannelBack      = "ch_back"
	CBChannelAnalytics = "ch_analytics" // вход в аналитику/UTM канала
	CBChannelRefresh   = "ch_refresh"   // 🔄 обновить статус админства бота во всех каналах
	CBChannelToggleSub = "ch_toggle_sub:" // + channel_id — тоггл канала подписки (пикер)
	CBChannelTogglePub = "ch_toggle_pub:" // + channel_id — тоггл канала публикации (пикер)

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
	CBFSMHint     = "fsm_hint"     // 💡 подсказка по текущему шагу (сообщением)
	CBFSMContinue = "fsm_continue" // ▶ продолжить после подсказки (возврат к шагу)

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

// replyMenuTexts — подписи кнопок главного меню. Используются как текстовый
// fallback: если сообщение от кнопки меню пришло как обычный текст, оно
// маршрутизируется через ReplyPayloadByText.
//
// NOTE: настоящий reply-keyboard (кнопки у поля ввода) в MAX для ботов не
// работает: платформа принимает attachment (200 OK), но молча выкидывает его
// (проверено живым API-запросом). Меню — inline.
var replyMenuTexts = []string{
	"🎁 Создать розыгрыш",
	"📋 Мои розыгрыши",
	"📢 Мои каналы",
	"🎟 Мои участия",
	"📊 Аналитика",
	"🆘 Поддержка",
}

// ReplyPayloadByText — payload действия по подписи кнопки меню ("", если не
// кнопка меню). Fallback для текстовых сообщений.
func ReplyPayloadByText(text string) string {
	switch text {
	case "🎁 Создать розыгрыш":
		return CBCreateGiveaway
	case "📋 Мои розыгрыши":
		return CBMyGiveaways
	case "📢 Мои каналы":
		return CBMyChannels
	case "🎟 Мои участия":
		return CBMyParticipations
	case "📊 Аналитика":
		return CBAnalytics
	case "🆘 Поддержка":
		return CBSupport
	}
	return ""
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

// FirstStepKeyboard — первый шаг создания: 💡 + Отменить (Назад идти некуда).
func FirstStepKeyboard() *model.Keyboard {
	kb := model.NewKeyboard()
	kb.AddRow().
		AddCallBack("💡", CBFSMHint).
		AddCallBack("✖ Отменить", CBFSMCancel)
	return kb
}

// HintKeyboard — под сообщением-подсказкой: продолжить с того места, где
// нажали лампочку, или отменить создание.
func HintKeyboard() *model.Keyboard {
	kb := model.NewKeyboard()
	kb.AddRow().
		AddCallBack("▶ Продолжить", CBFSMContinue).
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

// ConfirmErrorKeyboard — ошибка валидации при Принять: вернуться к шагу
// (fsm_back с предпросмотра ведёт на последний содержательный шаг) или отменить.
func ConfirmErrorKeyboard(giveawayID int64) *model.Keyboard {
	kb := model.NewKeyboard()
	kb.AddRow().AddCallBack("✏ Исправить", CBFSMBack)
	kb.AddRow().AddCallBack("✖ Отменить", CBFSMCancel)
	return kb
}

// ParticipateKeyboardBuilder — кнопка «Участвовать (N)» под постом розыгрыша.
//
// CALLBACK-кнопка (part:<id>): message_callback приходит ВСЕГДА, в отличие от
// deep-link. Проверено живьём: повторный клик max.ru/<bot>/start/part_N у уже
// запущенного бота просто открывает диалог на клиенте — bot_started НЕ
// присылается (в логах ни одного вебхука на клик) → участие терялось.
// Ответ бота в личку (капча-картинка / карточка «Вы участвуете») открывает
// диалог с ботом — юзер «проваливается» в чат.
func ParticipateKeyboardBuilder(buttonText string, giveawayID int64, participants int) *model.Keyboard {
	kb := model.NewKeyboard()
	label := fmt.Sprintf("%s (%d)", buttonText, participants)
	kb.AddRow().AddCallBack(label, CBParticipate+strconv.FormatInt(giveawayID, 10))
	return kb
}

// MediaStepKeyboard — клавиатура шага медиа в создании розыгрыша.
// hasMedia=false → «⏭ Пропустить» (медиа опциональны);
// hasMedia=true  → «Далее →» (медиа уже добавлено — «Пропустить» сбивает
// с толку). Условие динамическое: в каждом черновике своё (удалили медиа
// в редактуре → снова «Пропустить»), никакого перманентного переименования.
func MediaStepKeyboard(hasMedia bool) *model.Keyboard {
	if hasMedia {
		kb := model.NewKeyboard()
		kb.AddRow().AddCallBack("Далее →", CBFSMSkip)
		kb.AddRow().
			AddCallBack("💡", CBFSMHint).
			AddCallBack("← Назад", CBFSMBack).
			AddCallBack("✖ Отменить", CBFSMCancel)
		return kb
	}
	return NavWithSkipKeyboard()
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
func GiveawayAttachments(buttonText string, giveawayID int64, mediaTokens []MediaTokenSimple, preview bool) []model.Attachment {
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
		kb = ParticipateKeyboardBuilder(buttonText, giveawayID, 0)
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

// MediaEditDoneKeyboard — «Готово» при вводе медиа в редактуре.
func MediaEditDoneKeyboard(giveawayID int64) *model.Keyboard {
	kb := model.NewKeyboard()
	kb.AddRow().AddCallBack("✅ Готово", CBGwMediaDone+strconv.FormatInt(giveawayID, 10))
	return kb
}

// AnalyticsKeyboard — навигация на экране аналитики розыгрыша.
func AnalyticsKeyboard(giveawayID int64) *model.Keyboard {
	id := strconv.FormatInt(giveawayID, 10)
	kb := model.NewKeyboard()
	kb.AddRow().
		AddCallBack("👥 Участники", CBGwParticipants+id).
		AddCallBack("🔄 Обновить", CBGwAnalytics+id)
	kb.AddRow().
		AddCallBack("📎 CSV участников", CBGwCSVParts+id).
		AddCallBack("← К розыгрышу", CBGwOpen+id)
	return kb
}

// CancelKeyboard — кнопка отмены произвольного режима.
func CancelKeyboard() *model.Keyboard {
	kb := model.NewKeyboard()
	kb.AddRow().AddCallBack("✖ Отмена", CBMainMenu)
	return kb
}

// PostRefKeyboard — шаг билдера трекинг-ссылки: пост-источник (пропуск/отмена).
func PostRefKeyboard() *model.Keyboard {
	kb := model.NewKeyboard()
	kb.AddRow().AddCallBack("⏭ Без поста-источника", CBUTMSkipPost)
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
