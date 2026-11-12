package util

import (
	"crypto/rand"
	"encoding/hex"
	"fmt"
	"strconv"
	"strings"
	"time"

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
	CBGwCopy         = "gw_copy:" // создать копию завершённого розыгрыша
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
	CBUTMQuickCreate = "utm_quick"      // трекинг: выбор канала (главное меню)
	CBUTMQuickChSel  = "utm_qch:"       // + channel_id — канал выбран
	CBUTMBuildYes    = "utm_build_yes"  // после названия: настроить метки конструктором
	CBUTMBuildNo     = "utm_build_no"   // после названия: простая ссылка
	CBUTMStageSkip   = "utm_stage_skip" // билдер: пропустить текущий шаг
	CBUTMGW          = "utm_gw:"        // + giveaway_id — привязать ссылку к розыгрышу
	CBUTMGWSkip      = "utm_gw_skip"    // билдер: без привязки к розыгрышу
	CBUTMSkipPost    = "utm_post_skip"  // легаси: пропуск поста (маппится в stage-skip)
	CBUTMSkipCost    = "utm_cost_skip"  // легаси: без стоимости

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
	CBUTMDyn      = "utm_dyn:" // + channel_id — динамика подписчиков канала
	CBUTMExport   = "utm_exp:" // + channel_id — экспорт CSV

	// FSM создания (мастер по мануалу Claude-log)
	CBFSMBack     = "fsm_back"
	CBFSMCancel   = "fsm_cancel"
	CBFSMNext     = "fsm_next"     // «Далее →»: подтвердить шаг
	CBFSMExitYes  = "fsm_exit_yes" // подтверждение «Да, выйти»
	CBFSMOption   = "fsm_opt:"     // + key[:arg] — выбор варианта шага
	CBFSMConfirm  = "fsm_confirm"
	CBFSMReject   = "fsm_reject"
	CBFSMHint     = "fsm_hint"     // 💡 подсказка (notification, ≤150 символов)
	CBFSMContinue = "fsm_continue" // ▶ продолжить (возврат к шагу)
	CBFSMEdit     = "fsm_edit:"    // + step — правка пункта с экрана проверки
	CBFSMSave     = "fsm_save"     // 💾 «В черновики» с экрана проверки
	CBCal         = "cal:"         // + m/d/t/noop/past/back — календарь итогов

	// Кнопка "Участвовать" под постом розыгрыша
	CBParticipate = "part:" // + giveaway_id
)

// MainMenuKeyboard — главное меню. Высоту кнопок и перенос текста платформа
// MAX не позволяет менять (обрезает) — ключевые разделы полноширинными рядами.
func MainMenuKeyboard() *model.Keyboard {
	kb := model.NewKeyboard()
	kb.AddRow().
		AddCallBack("Создать 🎲", CBCreateGiveaway).
		AddCallBack("Трекинг 🔗", CBUTMQuickCreate)
	kb.AddRow().
		AddCallBack("Мои розыгрыши", CBMyGiveaways).
		AddCallBack("Аналитика", CBAnalytics)
	kb.AddRow().AddCallBack("Мои каналы", CBMyChannels)
	kb.AddRow().
		AddCallBack("Мои участия", CBMyParticipations).
		AddCallBack("Поддержка", CBSupport)
	return kb
}

// MainMenuWithDraftKeyboard — меню + строка «📝 Продолжить черновик»
// (мануал §11: появляется, когда есть незаконченное создание).
func MainMenuWithDraftKeyboard() *model.Keyboard {
	kb := model.NewKeyboard()
	kb.AddRow().AddCallBack("Продолжить черновик 📝", CBGwList)
	kb.AddRow().
		AddCallBack("Создать 🎲", CBCreateGiveaway).
		AddCallBack("Трекинг 🔗", CBUTMQuickCreate)
	kb.AddRow().
		AddCallBack("Мои розыгрыши", CBMyGiveaways).
		AddCallBack("Аналитика", CBAnalytics)
	kb.AddRow().AddCallBack("Мои каналы", CBMyChannels)
	kb.AddRow().
		AddCallBack("Мои участия", CBMyParticipations).
		AddCallBack("Поддержка", CBSupport)
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
	"Создать 🎲",
	"Трекинг 🔗",
	"Мои каналы",
	"Мои розыгрыши",
	"Аналитика",
	"Мои участия",
	"Поддержка",
}

// ReplyPayloadByText — payload действия по подписи кнопки меню ("", если не
// кнопка меню). Fallback для текстовых сообщений.
func ReplyPayloadByText(text string) string {
	switch text {
	case "Создать 🎲":
		return CBCreateGiveaway
	case "Трекинг 🔗":
		return CBUTMQuickCreate
	case "Мои каналы":
		return CBMyChannels
	case "Мои розыгрыши":
		return CBMyGiveaways
	case "Аналитика":
		return CBAnalytics
	case "Мои участия":
		return CBMyParticipations
	case "Поддержка":
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

// AddServiceRow дописывает служебный ряд шага мастера (мануал §3):
// «💡 Справка · ← Назад · ✕ Отмена». withBack=false на первом шаге
// (назад идти некуда — остаётся 💡 и ✕).
func AddServiceRow(kb *model.Keyboard, withBack bool) {
	row := kb.AddRow()
	row.AddCallBack("💡 Справка", CBFSMHint)
	if withBack {
		row.AddCallBack("← Назад", CBFSMBack)
	}
	row.AddCallBack("✕ Отменить", CBFSMCancel)
}

// CancelConfirmKeyboard — экран «Остановить создание?» (мануал §3 ✕ Отмена):
// «Да, выйти» / «Продолжить».
func CancelConfirmKeyboard() *model.Keyboard {
	kb := model.NewKeyboard()
	kb.AddRow().
		AddCallBack("✖ Да, выйти", CBFSMExitYes).
		AddCallBack("▶ Продолжить", CBFSMContinue)
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

// StartappLink — диплинк открытия мини-аппа бота с параметром start_param.
// Механика офиц. (dev.max.ru/webapps): max.ru/<bot>?startapp=<param> открывает
// мини-апп БЕЗ чата бота; клиент передаёт подписанный init_data с user_id.
// Проверено живьём на трекинг-ссылках (startapp=u_<code>).
func StartappLink(botUsername, param string) string {
	return "https://max.ru/" + botUsername + "?startapp=" + param
}

// ParticipateKeyboardBuilder — кнопка «Участвовать (N)» под постом розыгрыша.
//
// Основной путь (botUsername известен): LINK-кнопка max.ru/<bot>?startapp=part_<id>
// — открывает мини-апп участия (страница participate.html): подписки, капча и
// подтверждение там, чат бота не открывается, user_id приходит подписанным
// (init_data). Механика startapp-ссылок проверена живьём на трекинге.
//
// Fallback (имя бота ещё не узнали): CALLBACK-кнопка (part:<id>) — участие в
// чате бота (капча картинкой). Callback приходит всегда; ответ бота в личку
// открывает диалог. Так остаются рабочими и старые опубликованные посты.
func ParticipateKeyboardBuilder(buttonText string, giveawayID int64, participants int, botUsername string) *model.Keyboard {
	kb := model.NewKeyboard()
	label := fmt.Sprintf("%s (%d)", buttonText, participants)
	if botUsername != "" {
		kb.AddRow().AddLink(label, StartappLink(botUsername, "part_"+strconv.FormatInt(giveawayID, 10)))
		return kb
	}
	kb.AddRow().AddCallBack(label, CBParticipate+strconv.FormatInt(giveawayID, 10))
	return kb
}

// ResultsKeyboard — кнопка «🏆 Проверить результаты» под постом с итогами
// (P4): открывает страницу результатов в мини-аппе. Пустой username → nil
// (кнопки нет — итоги и так текстом в посте).
func ResultsKeyboard(giveawayID int64, botUsername string) *model.Keyboard {
	if botUsername == "" {
		return nil
	}
	kb := model.NewKeyboard()
	kb.AddRow().AddLink("🏆 Проверить результаты", StartappLink(botUsername, "results_"+strconv.FormatInt(giveawayID, 10)))
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
		kb = ParticipateKeyboardBuilder(buttonText, giveawayID, 0, "")
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

// CostKeyboard — шаг билдера: стоимость выхода (пропуск/отмена).
func CostKeyboard() *model.Keyboard {
	kb := model.NewKeyboard()
	kb.AddRow().AddCallBack("⏭ Без стоимости", CBUTMSkipCost)
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

// ChannelUTMKeyboard — меню канала: создание ссылки (название → опциональный
// конструктор меток) + аналитика.
func ChannelUTMKeyboard(channelID int64) *model.Keyboard {
	id := strconv.FormatInt(channelID, 10)
	kb := model.NewKeyboard()
	kb.AddRow().AddCallBack("➕ Новая ссылка", CBUTMCreate+id)
	kb.AddRow().
		AddCallBack("📊 Статистика", CBUTMStats+id).
		AddCallBack("📈 Динамика", CBUTMDyn+id)
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

// TrackingLinkKeyboard — экран готовой трекинг-ссылки: скопировать,
// статистика канала, в меню.
func TrackingLinkKeyboard(link string, channelID int64) *model.Keyboard {
	kb := model.NewKeyboard()
	kb.AddRow().AddClipboard("📋 Скопировать ссылку", link)
	if channelID > 0 {
		kb.AddRow().AddCallBack("📊 Статистика канала", CBChannelUTM+strconv.FormatInt(channelID, 10))
	}
	kb.AddRow().AddCallBack("🏠 В меню", CBMainMenu)
	return kb
}

// RandomCode генерирует случайную строку заданной длины (hex).
func RandomCode(n int) string {
	b := make([]byte, n/2+1)
	_, _ = rand.Read(b)
	return hex.EncodeToString(b)[:n]
}

// ---------- Календарь итогов (мануал §6) ----------

// weekdaysUS — заголовок дней недели (Пн..Вс).
var calWeekdays = []string{"Пн", "Вт", "Ср", "Чт", "Пт", "Сб", "Вс"}

// CalMonthKeyboard — клавиатура месяца: [←, Ноябрь 2026, →] / Пн..Вс / дни
// по 7 в ряд (пустые и прошедшие — «·»), выход — к быстрым вариантам и
// служебный ряд. Самый длинный месяц: 11 рядов, 59 кнопок (лимит 30/210).
// offset — смещение месяца от текущего (0 = этот месяц; листается до 12).
func CalMonthKeyboard(offset int) *model.Keyboard {
	now := time.Now().In(MoscowLocation())
	first := time.Date(now.Year(), now.Month(), 1, 0, 0, 0, 0, MoscowLocation()).AddDate(0, offset, 0)

	kb := model.NewKeyboard()
	// Строка 1: навигация по месяцам.
	nav := kb.AddRow()
	if offset > 0 {
		nav.AddCallBack("←", fmt.Sprintf("%sm:%d", CBCal, offset-1))
	} else {
		nav.AddCallBack("·", CBCal+"noop")
	}
	nav.AddCallBack("·"+first.Format("Январь 2006")+"·", CBCal+"noop")
	if offset < 12 {
		nav.AddCallBack("→", fmt.Sprintf("%sm:%d", CBCal, offset+1))
	} else {
		nav.AddCallBack("·", CBCal+"noop")
	}
	// Строка 2: дни недели (пустые ответы).
	wd := kb.AddRow()
	for _, d := range calWeekdays {
		wd.AddCallBack(d, CBCal+"noop")
	}
	// Дни: понедельник — первый столбец.
	lead := int(first.Weekday()+6) % 7 // Пн=0
	daysInMonth := first.AddDate(0, 1, -1).Day()
	var row *model.KeyboardRow
	for i := 0; i < lead; i++ {
		if i%7 == 0 {
			row = kb.AddRow()
		}
		row.AddCallBack("·", CBCal+"noop")
	}
	for d := 1; d <= daysInMonth; d++ {
		if (lead+d-1)%7 == 0 {
			row = kb.AddRow()
		}
		date := time.Date(first.Year(), first.Month(), d, 0, 0, 0, 0, MoscowLocation())
		if date.Before(time.Date(now.Year(), now.Month(), now.Day(), 0, 0, 0, 0, MoscowLocation())) {
			row.AddCallBack("·", CBCal+"past") // прошедший день
			continue
		}
		row.AddCallBack(fmt.Sprintf("%d", d), fmt.Sprintf("%sd:%s", CBCal, date.Format("2006-01-02")))
	}
	kb.AddRow().AddCallBack("← К быстрым вариантам", CBCal+"back")
	AddServiceRow(kb, true)
	return kb
}

// CalTimeKeyboard — экран выбора времени дня: 6 быстрых часов двумя рядами,
// своё время — текстом (ЧЧ:ММ). date — выбранный день (YYYY-MM-DD).
func CalTimeKeyboard(date string) *model.Keyboard {
	kb := model.NewKeyboard()
	r1 := kb.AddRow()
	for _, h := range []string{"12:00", "15:00", "18:00"} {
		r1.AddCallBack(h, fmt.Sprintf("%st:%sT%s", CBCal, date, h))
	}
	r2 := kb.AddRow()
	for _, h := range []string{"20:00", "21:00", "22:00"} {
		r2.AddCallBack(h, fmt.Sprintf("%st:%sT%s", CBCal, date, h))
	}
	kb.AddRow().AddCallBack("← Другой день", fmt.Sprintf("%sm:0", CBCal))
	AddServiceRow(kb, true)
	return kb
}

// ChannelURL — абсолютный URL публичного канала по его link из MAX API
// (link приходит как username/слаг, но на всякий случай допускаем и полный
// URL). Пустая строка — канала без публичной ссылки (приватный).
func ChannelURL(link string) string {
	link = strings.TrimSpace(link)
	if link == "" {
		return ""
	}
	if IsHTTPURL(link) {
		return link
	}
	return "https://max.ru/" + strings.TrimPrefix(link, "@")
}

// IsHTTPURL — полный http(s)-адрес (пост-источник-ссылка ведёт посетителя
// на конкретный пост, а не просто на канал).
func IsHTTPURL(s string) bool {
	return strings.HasPrefix(s, "http://") || strings.HasPrefix(s, "https://")
}

// ChannelEntryKeyboard — экран посетителя, пришедшего по трекинг-ссылке.
// Приоритет кнопки: КОНКРЕТНЫЙ ПОСТ (если пост-источник — ссылка), затем
// подписка на сам канал; + возврат в меню бота.
func ChannelEntryKeyboard(channelTitle, channelURL, postURL string) *model.Keyboard {
	kb := model.NewKeyboard()
	if postURL != "" {
		kb.AddRow().AddLink("📄 Открыть пост", postURL)
	}
	if channelURL != "" {
		label := "📢 Подписаться на канал"
		if channelTitle != "" {
			label = "📢 Подписаться на «" + channelTitle + "»"
		}
		kb.AddRow().AddLink(label, channelURL)
	}
	kb.AddRow().AddCallBack("🏠 В меню", CBMainMenu)
	return kb
}
