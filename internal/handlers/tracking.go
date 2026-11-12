package handlers

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/maxbot/giveawaybot/internal/store"
	"github.com/maxbot/giveawaybot/internal/util"
	"github.com/max-messenger/max-bot-api-client-go/v2/model"
)

const utmPayloadPrefix = "utm_" // payload диплинка: utm_<code>

// onUserAdded — юзер вступил в канал, где бот админ.
// Атрибуция UTM (если юзер пришёл по UTM-ссылке) + лог подписки + подписчик
// ссылки (retention-таблица, миграция 0014).
func (b *Bot) onUserAdded(ctx context.Context, u model.Update) {
	userID := u.UserID
	chatID := u.ChatID
	if userID == 0 || chatID == 0 {
		return
	}
	// Сохраняем/обновляем вступившего юзера.
	usr := u.GetUser()
	if usr.UserID != 0 {
		_ = b.db.UpsertUser(ctx, store.User{
			UserID:    usr.UserID,
			FirstName: usr.FirstName,
			Username:  usr.Username,
			IsBot:     usr.IsBot,
		})
	}
	// Атрибуция: был ли юзер ранее по UTM-ссылке на этот канал.
	link, attributed, err := b.db.UTMAccountSignup(ctx, userID, chatID)
	if err != nil {
		b.log.Debug("utm account signup", "err", err)
	}
	if attributed {
		// Подписчик ссылки: retention-таблица (отписки трекаем событием и
		// суточным поллингом).
		if err := b.db.UpsertUTMSubscriber(ctx, link.ID, userID, chatID); err != nil {
			b.log.Debug("utm subscriber upsert", "err", err, "link", link.ID, "user", userID)
		}
	}
	// Лог подписки (связь канал-юзер). Канал ищем по chat_id среди всех владельцев.
	source := "post"
	if attributed {
		source = "utm"
	}
	_ = b.db.LogChannelJoin(ctx, userID, chatID, source)
}

// onUserRemoved — юзер покинул канал. Фиксируем отписку везде: лог, счётчик
// ссылки, retention-записи.
func (b *Bot) onUserRemoved(ctx context.Context, u model.Update) {
	userID := u.UserID
	chatID := u.ChatID
	if userID == 0 || chatID == 0 {
		return
	}
	_ = b.db.MarkUnsubscribedByChat(ctx, userID, chatID)
	_ = b.db.UTMAccountUnsub(ctx, userID, chatID)
	_ = b.db.MarkUTMUnsubscribedByUser(ctx, userID, chatID)
}

// onBotStartedPayload — обработка payload диплинка из bot_started
// (UTM-ссылки; part_ — легаси-посты с link-кнопкой участия).
func (b *Bot) onBotStartedPayload(ctx context.Context, userID int64, payload string) {
	if payload == "" {
		return
	}
	b.log.Info("deeplink start", "user", userID, "payload", payload)
	// Легаси-путь: пост со link-кнопкой участия (part_<id>) — обрабатываем
	// как обычное нажатие «Участвовать».
	if strings.HasPrefix(payload, "part_") {
		idStr := strings.TrimPrefix(payload, "part_")
		b.onParticipate(ctx, userID, idStr, "")
		return
	}
	// Реферальная ссылка друга (ref_<code>): фиксируем приглашение — +1 билет
	// инвайтеру начисляется, когда приглашённый станет участником.
	if strings.HasPrefix(payload, "ref_") {
		code := strings.TrimPrefix(payload, "ref_")
		if link, err := b.db.GetReferralLinkByCode(ctx, code); err == nil {
			if err := b.db.SaveReferralInvite(ctx, link.GiveawayID, link.UserID, userID); err != nil {
				b.log.Debug("referral invite", "err", err, "code", code)
			} else {
				b.log.Info("referral invite", "giveaway", link.GiveawayID, "inviter", link.UserID, "invitee", userID)
			}
		}
		b.sendStartScreen(ctx, userID)
		return
	}
	// UTM-диплинк.
	code := strings.TrimPrefix(payload, utmPayloadPrefix)
	if code == "" || code == payload {
		// Не UTM и не part_ — обычный запуск: показываем стартовый экран.
		b.sendStartScreen(ctx, userID)
		return
	}
	link, err := b.db.GetUTMLinkByCode(ctx, code)
	if err != nil {
		b.log.Debug("utm link not found", "code", code, "err", err)
		// Код битый/устаревший — юзер всё равно пришёл: показываем бот.
		b.sendStartScreen(ctx, userID)
		return
	}
	if err := b.db.UTMRecordClick(ctx, link.ID, userID); err != nil {
		b.log.Warn("utm record click", "err", err)
	}
	// Диалог только что открылся ссылкой. Ссылка, привязанная к каналу, должна
	// приводить посетителя В КАНАЛ (кнопка-ссылка на канал), а не в меню бота —
	// иначе воронка обрывается в чате бота и подписка не случается.
	if link.ChannelID != 0 {
		b.sendUTMChannelScreen(ctx, userID, link)
		return
	}
	b.sendStartScreen(ctx, userID)
}

// sendUTMChannelScreen — экран для посетителя, пришедшего по трекинг-ссылке
// канала: кнопки ведут на КОНКРЕТНЫЙ ПОСТ (если пост-источник — ссылка)
// и/или на сам канал. Диплинк технически
// открывает диалог с ботом (в MAX только так боту сообщается клик — прямые
// ссылки каналов payload не несут), но «оседать» посетитель должен в канале.
// Клик уже засчитан в onBotStartedPayload; подписка атрибутируется по
// user_added (бот админ в канале).
func (b *Bot) sendUTMChannelScreen(ctx context.Context, userID int64, link store.UTMLink) {
	ch, err := b.db.GetChannel(ctx, link.ChannelID, link.OwnerUserID)
	if err != nil {
		// Канал удалён у владельца — показываем обычный стартовый экран.
		b.sendStartScreen(ctx, userID)
		return
	}
	title := ifEmpty(ch.Title, fmt.Sprintf("канал #%d", ch.ChatID))
	channelURL := util.ChannelURL(ch.Link)
	// Пост-источник-ссылка: посетитель идёт на КОНКРЕТНЫЙ ПОСТ, не на канал.
	postURL := ""
	if ref := strings.TrimSpace(link.PostRef); util.IsHTTPURL(ref) {
		postURL = ref
	}

	if channelURL != "" || postURL != "" {
		action := "Откройте канал и подпишитесь — кнопка ниже 👇"
		if postURL != "" {
			action = "Откройте пост и подпишитесь на канал — кнопки ниже 👇"
		}
		_, _ = b.api.SendTextWithKeyboard(ctx, userID,
			fmt.Sprintf("👋 Привет!\n\nВы пришли по ссылке на канал «%s».\n%s", title, action),
			util.ChannelEntryKeyboard(title, channelURL, postURL))
		return
	}
	// Приватный канал без публичной ссылки и без поста: кнопки невозможны.
	_, _ = b.api.SendTextWithKeyboard(ctx, userID,
		fmt.Sprintf("👋 Привет!\n\nВы пришли по ссылке на канал «%s».\n"+
			"Найдите его в поиске MAX по названию и подпишитесь.", title),
		util.BackKeyboard())
}

// costOf — стоимость из сессии билдера (0 если не вводили).
func costOf(sess store.UserSession) float64 {
	if c, ok := sess.Payload["cost"].(float64); ok {
		return c
	}
	return 0
}

// showUTMMenu — меню конкретного канала: генерация UTM / аналитика.
func (b *Bot) showUTMMenu(ctx context.Context, uid int64, p string) {
	idStr := strings.TrimPrefix(p, util.CBChannelUTM)
	chID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return
	}
	ch, err := b.db.GetChannel(ctx, chID, uid)
	if err != nil {
		_ = b.api.SendText(ctx, uid, "Канал не найден.")
		return
	}
	title := ch.Title
	if title == "" {
		title = fmt.Sprintf("канал #%d", ch.ChatID)
	}

	// Загружаем статистику по каналу.
	stats, _ := b.db.ListUTMStats(ctx, uid, chID)
	var totClicks, totSignups, totUnsubs int64
	for _, s := range stats {
		totClicks += s.Clicks
		totSignups += s.Signups
		totUnsubs += s.Unsubs
	}
	conv := float64(0)
	if totClicks > 0 {
		conv = float64(totSignups) / float64(totClicks) * 100
	}

	// Прямая ссылка на канал (сразу в канал, без прослоек) — отдельно от
	// трекинг-ссылок: переходы по ней бот посчитать НЕ может (Bot API не
	// создаёт инвайт-ссылки и не видит вход по ссылке), зато юзер попадает
	// в канал мгновенно. Инвайт-ссылки с подсчётом админ создаёт руками
	// в настройках канала клиента MAX.
	kb := util.ChannelUTMKeyboard(chID)
	if url := util.ChannelURL(ch.Link); url != "" {
		kb.AddRow().AddClipboard("📋 Прямая ссылка (без счётчика)", url)
	}

	text := fmt.Sprintf("📊 %s\n\n", title) +
		fmt.Sprintf("Ссылок: %d\n", len(stats)) +
		fmt.Sprintf("Кликов: %d\n", totClicks) +
		fmt.Sprintf("Подписок: %d\n", totSignups) +
		fmt.Sprintf("Отписок: %d\n", totUnsubs) +
		fmt.Sprintf("Конверсия: %.1f%%\n\n", conv) +
		"⚡ Ссылки бесшовные: юзер кликает → короткая заставка → сразу канал/пост.\n" +
		"Клик и подписка засчитываются с его id (мини-апп)."

	_, _ = b.api.SendTextWithKeyboard(ctx, uid, text, kb)
}

// startQuickUTM — БЫСТРАЯ трекинг-ссылка (P6): выбор канала из «мои каналы» →
// название → бот выдаёт ссылку. Весь трафик идёт через мини-апп (startapp):
// бот запоминает id кликнувшего и ведёт его в канал, не открывая чат.
func (b *Bot) startQuickUTM(ctx context.Context, uid int64) {
	channels, err := b.db.ListChannels(ctx, uid)
	if err != nil || len(channels) == 0 {
		_, _ = b.api.SendTextWithKeyboard(ctx, uid,
			"🔗 Трекинг\n\nСначала добавьте канал в «Мои каналы» — куда должна вести ссылка.",
			util.BackKeyboard())
		return
	}
	var admin []store.Channel
	for _, c := range channels {
		if c.IsBotAdmin {
			admin = append(admin, c)
		}
	}
	if len(admin) == 0 {
		_, _ = b.api.SendTextWithKeyboard(ctx, uid,
			"🔗 Трекинг\n\nНет каналов, где бот админ. Сначала добавьте бота админом в канал.",
			util.BackKeyboard())
		return
	}
	kb := model.NewKeyboard()
	for _, c := range admin {
		kb.AddRow().AddCallBack("📢 "+ifEmpty(c.Title, fmt.Sprintf("канал #%d", c.ChatID)),
			util.CBUTMQuickChSel+strconv.FormatInt(c.ID, 10))
	}
	kb.AddRow().AddCallBack("✖ Отмена", util.CBMainMenu)
	_, _ = b.api.SendTextWithKeyboard(ctx, uid,
		"🔗 Трекинг — быстрая ссылка\n\nВыберите канал, на который будет вести ссылка:",
		kb)
}

// pickQuickUTMChannel — канал выбран (P6, шаг 1/2): запоминаем и спрашиваем
// название ссылки (шаг 2/2) — после ввода бот сразу выдаёт ссылку.
func (b *Bot) pickQuickUTMChannel(ctx context.Context, uid int64, chID int64) {
	// Канал должен принадлежать юзеру — crafted-callback с чужим channel_id
	// иначе привязал бы трекинг-ссылку к чужому каналу.
	ch, err := b.db.GetChannel(ctx, chID, uid)
	if err != nil {
		_ = b.api.SendText(ctx, uid, "Канал не найден.")
		return
	}
	if err := b.db.SetSessionPayload(ctx, uid, store.SessionUTMCreate, map[string]any{
		"channel_id": float64(chID), "quick": true,
	}); err != nil {
		b.log.Warn("utm: set session", "err", err, "user", uid)
		_ = b.api.SendText(ctx, uid, "⚠️ Не удалось начать создание ссылки. Попробуйте позже.")
		return
	}
	_, _ = b.api.SendTextWithKeyboard(ctx, uid,
		fmt.Sprintf("🔗 Канал «%s» выбран.\n\n"+
			"Отправьте НАЗВАНИЕ ссылки — как её искать в статистике.\n"+
			"Например: «Telegram», «Реклама у Иванова», «Сторис 15.11»",
			ifEmpty(ch.Title, fmt.Sprintf("канал #%d", ch.ChatID))),
		util.CancelKeyboard())
}

// startCreateUTM — вход из меню канала: имя → вопрос «Настроить метки?»
// (билдер — опциональное продолжение быстрого флоу).
func (b *Bot) startCreateUTM(ctx context.Context, uid int64, p string) {
	idStr := strings.TrimPrefix(p, util.CBUTMCreate)
	chID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return
	}
	// Канал должен принадлежать юзеру — crafted-callback с чужим channel_id
	// иначе привязал бы трекинг-ссылку к чужому каналу.
	if _, err := b.db.GetChannel(ctx, chID, uid); err != nil {
		_ = b.api.SendText(ctx, uid, "Канал не найден.")
		return
	}
	if err := b.db.SetSessionPayload(ctx, uid, store.SessionUTMCreate, map[string]any{"channel_id": float64(chID)}); err != nil {
		b.log.Warn("utm: set session", "err", err, "user", uid)
		_ = b.api.SendText(ctx, uid, "⚠️ Не удалось начать создание ссылки. Попробуйте позже.")
		return
	}
	_, _ = b.api.SendTextWithKeyboard(ctx, uid,
		"🔗 Новая трекинг-ссылка\n\n"+
			"Отправьте НАЗВАНИЕ ссылки — как её искать в статистике.\n"+
			"Например: «Telegram», «Реклама у Иванова», «Сторис 15.11»",
		util.CancelKeyboard())
}

// askUTMBuilder — после названия: использовать конструктор меток или простая
// ссылка? (по ТЗ: сначала всё как обычно, потом опциональный билдер).
func (b *Bot) askUTMBuilder(ctx context.Context, uid int64) {
	kb := model.NewKeyboard()
	kb.AddRow().AddCallBack("⚙️ Настроить метки", util.CBUTMBuildYes)
	kb.AddRow().AddCallBack("✅ Простая ссылка", util.CBUTMBuildNo)
	kb.AddRow().AddCallBack("✖ Отмена", util.CBMainMenu)
	_, _ = b.api.SendTextWithKeyboard(ctx, uid,
		"⚙️ **Настроить ссылку конструктором меток?**\n\n"+
			"Конструктор добавит к ссылке (все шаги можно пропустить):\n"+
			"• **UTM-метки** — utm_source (источник), utm_medium (тип трафика), "+
			"utm_campaign (кампания), utm_term (ключевая фраза), utm_content (элемент, A/B)\n"+
			"• **Ссылку на конкретный пост** — переход ведёт на выбранный пост канала\n"+
			"• **Привязку к розыгрышу** — в аналитике будет видно, сколько подписчиков "+
			"ОСТАЛОСЬ после его окончания\n"+
			"• **Стоимость** — посчитаем цену подписчика (CPA)\n\n"+
			"Простая ссылка — только название и канал, как раньше.",
		kb)
}

// utmBuilderStages — порядок шагов билдера (все опциональны).
var utmBuilderStages = []string{"src", "med", "cmp", "term", "cont", "post", "gw", "cost"}

// utmStagePrompt — текст-приглашение шага билдера (по таблице UTM).
func utmStagePrompt(stage, name string) (string, string) {
	switch stage {
	case "src":
		return "Шаг 1/8 · utm_source", "Источник трафика — сайт или канал, откуда идут переходы " +
			"(обязательная метка). По умолчанию = названию ссылки «" + name + "»."
	case "med":
		return "Шаг 2/8 · utm_medium", "Тип трафика / рекламная модель. Например: cpc (за клик), " +
			"post (пост), story (сторис), email."
	case "cmp":
		return "Шаг 3/8 · utm_campaign", "Название рекламной кампании. Например: spring_sale, запуск_ноябрь."
	case "term":
		return "Шаг 4/8 · utm_term", "Ключевая фраза кампании (для контекста). Например: розыгрыш+iphone."
	case "cont":
		return "Шаг 5/8 · utm_content", "Элемент, на который кликнули — для A/B-теста. " +
			"Например: banlink (баннер) или textlink (текстовая ссылка)."
	case "post":
		return "Шаг 6/8 · Пост-источник", "Ссылка на конкретный пост (https://max.ru/…) — переход по " +
			"трекинг-ссылке будет вести на него, а не на канал. Или короткое описание размещения."
	case "gw":
		return "Шаг 7/8 · Розыгрыш", "Привязать ссылку к розыгрышу: после его окончания покажем, " +
			"сколько подписчиков осталось (отток после розыгрыша)."
	case "cost":
		return "Шаг 8/8 · Стоимость, ₽", "Сколько заплачено за размещение — посчитаем CPA (цену подписчика)."
	}
	return "", ""
}

// showUTMStage — показать шаг билдера (кнопка «⏭ Пропустить» у всех).
func (b *Bot) showUTMStage(ctx context.Context, uid int64, sess store.UserSession, stage string) {
	name, _ := sess.Payload["source"].(string)
	title, text := utmStagePrompt(stage, name)
	kb := model.NewKeyboard()
	kb.AddRow().AddCallBack("⏭ Пропустить", util.CBUTMStageSkip)
	kb.AddRow().AddCallBack("✖ Отмена", util.CBMainMenu)
	_, _ = b.api.SendTextWithKeyboard(ctx, uid,
		"⚙️ **"+title+"**\n\n"+text+"\n\nОтправьте значение сообщением или нажмите «⏭ Пропустить».", kb)
}

// showUTMGiveawayPicker — шаг 7: выбор розыгрыша для привязки.
func (b *Bot) showUTMGiveawayPicker(ctx context.Context, uid int64) {
	items, err := b.db.ListGiveaways(ctx, uid, "")
	if err != nil || len(items) == 0 {
		_ = b.api.SendText(ctx, uid, "У вас пока нет розыгрышей — привязка недоступна, пропускаю шаг.")
		b.utmStageSkip(ctx, uid)
		return
	}
	kb := model.NewKeyboard()
	shown := 0
	for _, g := range items {
		if shown >= 20 {
			continue
		}
		// Черновики тоже можно привязать (ссылка живёт дольше розыгрыша:
		// привязка к черновику = ссылка на грядущий розыгрыш).
		mark := map[string]string{
			store.StatusDraft: " 📝", store.StatusScheduled: " 📅",
			store.StatusActive: " 🟢", store.StatusFinished: " 🏁",
			store.StatusCancelled: " ✖",
		}[g.Status]
		kb.AddRow().AddCallBack(ifEmpty(g.Name, "без названия")+mark,
			util.CBUTMGW+strconv.FormatInt(g.ID, 10))
		shown++
	}
	kb.AddRow().AddCallBack("⏭ Без розыгрыша", util.CBUTMGWSkip)
	kb.AddRow().AddCallBack("✖ Отмена", util.CBMainMenu)
	_, _ = b.api.SendTextWithKeyboard(ctx, uid,
		"⚙️ **Шаг 7/8 · Розыгрыш**\n\nВыберите розыгрыш, к которому привязать ссылку "+
			"(или пропустите):", kb)
}

// utmNextStage — сохранить значение текущего шага и перейти к следующему
// (value="" — пропуск шага; stage="cost" value — строка-число).
func (b *Bot) utmNextStage(ctx context.Context, uid int64, sess store.UserSession, stage, value string) {
	payload := map[string]any{"stage": nextUTMStage(stage)}
	for k, v := range sess.Payload {
		if k != "stage" && k != "quick" {
			payload[k] = v
		}
	}
	if value != "" {
		switch stage {
		case "src":
			payload["src_custom"] = value
		case "med":
			payload["medium"] = value
		case "cmp":
			payload["campaign"] = value
		case "term":
			payload["utm_term"] = value
		case "cont":
			payload["utm_content"] = value
		case "post":
			payload["post_ref"] = value
		case "gw":
			if gid, err := strconv.ParseInt(value, 10, 64); err == nil {
				payload["giveaway_id"] = float64(gid)
			}
		}
	}
	if err := b.db.SetSessionPayload(ctx, uid, store.SessionUTMCreate, payload); err != nil {
		b.log.Warn("utm: stage save", "err", err, "user", uid)
	}
	next, _ := payload["stage"].(string)
	if next == "" {
		b.finishUTMLinkFromSession(ctx, uid)
		return
	}
	nsess, _ := b.db.GetSessionPayload(ctx, uid)
	if next == "gw" {
		b.showUTMGiveawayPicker(ctx, uid)
		return
	}
	b.showUTMStage(ctx, uid, nsess, next)
}

// nextUTMStage — следующий шаг билдера ("" = конец).
func nextUTMStage(stage string) string {
	for i, s := range utmBuilderStages {
		if s == stage && i+1 < len(utmBuilderStages) {
			return utmBuilderStages[i+1]
		}
	}
	return ""
}

// utmStageSkip — «⏭ Пропустить» текущий шаг билдера.
func (b *Bot) utmStageSkip(ctx context.Context, uid int64) {
	sess, err := b.db.GetSessionPayload(ctx, uid)
	if err != nil || sess.State != store.SessionUTMCreate {
		return
	}
	stage, _ := sess.Payload["stage"].(string)
	b.utmNextStage(ctx, uid, sess, stage, "")
}

// pickUTMGiveaway — розыгрыш выбран для привязки ссылки.
func (b *Bot) pickUTMGiveaway(ctx context.Context, uid int64, gid int64) {
	sess, err := b.db.GetSessionPayload(ctx, uid)
	if err != nil || sess.State != store.SessionUTMCreate {
		return
	}
	if _, err := b.db.GetGiveaway(ctx, gid, uid); err != nil {
		_ = b.api.SendText(ctx, uid, "Розыгрыш не найден.")
		return
	}
	b.utmNextStage(ctx, uid, sess, "gw", strconv.FormatInt(gid, 10))
}

// handleUTMCreateInput — ввод в флоу создания ссылки: название → вопрос о
// билдере → шаги билдера (все опциональны).
func (b *Bot) handleUTMCreateInput(ctx context.Context, uid int64, text string) bool {
	sess, err := b.db.GetSessionPayload(ctx, uid)
	if err != nil || sess.State != store.SessionUTMCreate {
		_ = b.db.ClearSession(ctx, uid)
		return false
	}
	chIDf, ok := sess.Payload["channel_id"]
	if !ok {
		_ = b.db.ClearSession(ctx, uid)
		_ = b.api.SendText(ctx, uid, "Сессия утеряна, начните заново.")
		return true
	}
	_ = chIDf
	stage, _ := sess.Payload["stage"].(string)

	switch stage {
	case "", "name":
		// Название ссылки (оно же utm_source по умолчанию).
		source := strings.TrimSpace(text)
		if err := util.ValidateUTMSource(source); err != nil {
			_ = b.api.SendText(ctx, uid, "⚠️ "+err.Error())
			return true
		}
		_ = b.db.SetSessionPayload(ctx, uid, store.SessionUTMCreate, map[string]any{
			"channel_id": sess.Payload["channel_id"], "stage": "build_q", "source": source,
		})
		b.askUTMBuilder(ctx, uid)
		return true

	case "src", "med", "cmp", "term", "cont":
		v := strings.TrimSpace(text)
		if v != "" {
			if err := util.ValidateUTMSource(v); err != nil {
				_ = b.api.SendText(ctx, uid, "⚠️ "+err.Error())
				return true
			}
		}
		b.utmNextStage(ctx, uid, sess, stage, v)
		return true

	case "post":
		postRef := strings.TrimSpace(text)
		if postRef != "" {
			if err := util.ValidatePostRef(postRef); err != nil {
				_ = b.api.SendText(ctx, uid, "⚠️ "+err.Error())
				return true
			}
		}
		b.utmNextStage(ctx, uid, sess, "post", postRef)
		return true

	case "cost":
		costText := strings.TrimSpace(strings.Replace(text, ",", ".", 1))
		cost := 0.0
		if costText != "" {
			var err error
			cost, err = strconv.ParseFloat(costText, 64)
			if err != nil || cost < 0 || cost > 10000000 {
				_ = b.api.SendText(ctx, uid, "⚠️ Введите число (рубли, можно с копейками) или нажмите «⏭ Пропустить».")
				return true
			}
		}
		payload := map[string]any{"stage": ""}
		for k, v := range sess.Payload {
			if k != "stage" {
				payload[k] = v
			}
		}
		if cost > 0 {
			payload["cost"] = cost
		}
		_ = b.db.SetSessionPayload(ctx, uid, store.SessionUTMCreate, payload)
		b.finishUTMLinkFromSession(ctx, uid)
		return true
	}
	return false
}

// finishUTMLinkFromSession — финал: создаёт ссылку из сессии (быстрая или
// собранная билдером — все пропущенные шаги = дефолты быстрой ссылки) и
// показывает ОДНУ startapp-ссылку (весь трафик через мини-апп).
func (b *Bot) finishUTMLinkFromSession(ctx context.Context, uid int64) {
	sess, err := b.db.GetSessionPayload(ctx, uid)
	if err != nil || sess.State != store.SessionUTMCreate {
		_ = b.api.SendText(ctx, uid, "Сессия утеряна, начните заново.")
		return
	}
	chIDf, _ := sess.Payload["channel_id"].(float64)
	chID := int64(chIDf)
	source, _ := sess.Payload["source"].(string)
	medium, _ := sess.Payload["medium"].(string)
	campaign, _ := sess.Payload["campaign"].(string)
	term, _ := sess.Payload["utm_term"].(string)
	content, _ := sess.Payload["utm_content"].(string)
	postRef, _ := sess.Payload["post_ref"].(string)
	srcCustom, _ := sess.Payload["src_custom"].(string)
	gwF, _ := sess.Payload["giveaway_id"].(float64)
	giveawayID := int64(gwF)
	cost := 0.0
	if c, ok := sess.Payload["cost"].(float64); ok {
		cost = c
	}

	if srcCustom != "" {
		source = srcCustom
	}
	// Дефолты быстрой ссылки: все пропущенные шаги не меняют результат.
	if medium == "" {
		medium = "referral"
	}
	if campaign == "" {
		campaign = source
		if giveawayID != 0 {
			campaign = fmt.Sprintf("giveaway_%d", giveawayID)
		}
	}

	code := util.RandomCode(10)
	if _, err := b.db.CreateUTMLink(ctx, uid, chID, source, medium, campaign,
		term, content, postRef, code, cost, giveawayID); err != nil {
		b.log.Warn("utm: create link", "err", err, "user", uid, "channel", chID)
		_ = b.api.SendText(ctx, uid, "⚠️ Не удалось создать ссылку. Попробуйте позже.")
		return
	}
	_ = b.db.ClearSession(ctx, uid)

	botName := b.botUsername
	if botName == "" {
		b.log.Warn("botUsername не задан — ссылка будет нерабочей", "user", uid)
		botName = "ВАШ_БОТ"
	}
	link := util.StartappLink(botName, "u_"+code)

	// Что настроено билдером (показываем только непустое).
	var set []string
	if srcCustom != "" {
		set = append(set, "utm_source="+srcCustom)
	}
	if medium != "referral" {
		set = append(set, "utm_medium="+medium)
	}
	if campaign != source && campaign != fmt.Sprintf("giveaway_%d", giveawayID) {
		set = append(set, "utm_campaign="+campaign)
	}
	if term != "" {
		set = append(set, "utm_term="+term)
	}
	if content != "" {
		set = append(set, "utm_content="+content)
	}
	var lines string
	if len(set) > 0 {
		lines += "\nМетки: " + strings.Join(set, ", ")
	}
	if postRef != "" {
		lines += "\n📍 Ведёт на: " + postRef
	}
	if giveawayID != 0 {
		if g, err := b.db.GetGiveaway(ctx, giveawayID, uid); err == nil {
			lines += "\n🎯 Розыгрыш: " + ifEmpty(g.Name, "без названия") +
				" (после итогов покажем, сколько подписчиков осталось)"
		}
	}
	if cost > 0 {
		lines += fmt.Sprintf("\n💰 Стоимость: %.2f ₽ (CPA посчитается в статистике)", cost)
	}

	title := ""
	if ch, err := b.db.GetChannel(ctx, chID, uid); err == nil {
		title = ifEmpty(ch.Title, fmt.Sprintf("канал #%d", ch.ChatID))
	}
	_, _ = b.api.SendTextWithKeyboard(ctx, uid,
		fmt.Sprintf("✅ Ссылка для «%s» готова:\n\n⚡ %s\n\n"+
			"Клик → мини-апп запоминает id юзера → ведёт в канал/на пост. "+
			"Кто подписался и кто отписался — в статистике (проверяем подписки сутки за суткой 2 недели)."+
			"\nНазвание: %s%s",
			title, link, source, lines),
		util.TrackingLinkKeyboard(link, chID))
}

// showUTMStats — аналитика UTM по каналу.
func (b *Bot) showUTMStats(ctx context.Context, uid int64, p string) {
	idStr := strings.TrimPrefix(p, util.CBUTMStats)
	chID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return
	}
	stats, err := b.db.ListUTMStats(ctx, uid, chID)
	if err != nil {
		_ = b.api.SendText(ctx, uid, "⚠️ Ошибка загрузки статистики.")
		return
	}
	if len(stats) == 0 {
		_, _ = b.api.SendTextWithKeyboard(ctx, uid, "📊 UTM-ссылок пока нет. Создайте первую.", util.ChannelUTMKeyboard(chID))
		return
	}
	var sb strings.Builder
	sb.WriteString("📊 Трекинг-ссылки канала:\n\n")
	for _, st := range stats {
		conv := float64(0)
		if st.Clicks > 0 {
			conv = float64(st.Signups) / float64(st.Clicks) * 100
		}
		ret, err := b.db.GetUTMLinkRetention(ctx, st.ID)
		if err != nil {
			b.log.Debug("retention", "err", err, "link", st.ID)
		}
		fmt.Fprintf(&sb, "• %s\n", st.Source)
		fmt.Fprintf(&sb, "  клики: %d | подписки: %d | конверсия: %.0f%%\n",
			st.Clicks, st.Signups, conv)
		if ret.Subscribed > 0 {
			fmt.Fprintf(&sb, "  осталось: %d из %d (отписались: %d)\n",
				ret.StillIn, ret.Subscribed, ret.Subscribed-ret.StillIn)
			fmt.Fprintf(&sb, "  удержание: 1д %d · 3д %d · 7д %d · 14д %d\n",
				ret.Retained1d, ret.Retained3d, ret.Retained7d, ret.Retained14d)
		}
		if st.GiveawayID != 0 && ret.GiveawayEndAt != nil && ret.Subscribed > 0 {
			fmt.Fprintf(&sb, "  после розыгрыша осталось: %d из %d\n",
				ret.RetainedAfterGiveaway, ret.Subscribed)
		}
		if st.Cost > 0 {
			cpaDirty, cpaClean := 0.0, 0.0
			if ret.Subscribed > 0 {
				cpaDirty = st.Cost / float64(ret.Subscribed)
			}
			if ret.StillIn > 0 {
				cpaClean = st.Cost / float64(ret.StillIn)
			}
			fmt.Fprintf(&sb, "  CPA: грязный %.0f₽ (по всем подписавшимся) · чистый %.0f₽ (по оставшимся)\n",
				cpaDirty, cpaClean)
		}
		if st.PostRef != "" {
			fmt.Fprintf(&sb, "  пост: %s\n", st.PostRef)
		}
	}
	_, _ = b.api.SendTextWithKeyboard(ctx, uid, sb.String(), util.ChannelUTMStatsKeyboard(chID))
}

// exportUTMCSV — экспорт статистики UTM в CSV-файл (через MAX Upload API).
func (b *Bot) exportUTMCSV(ctx context.Context, uid int64, p string) {
	idStr := strings.TrimPrefix(p, util.CBUTMExport)
	chID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return
	}
	stats, err := b.db.ListUTMStats(ctx, uid, chID)
	if err != nil || len(stats) == 0 {
		_ = b.api.SendText(ctx, uid, "Нет данных для экспорта.")
		return
	}
	var csv strings.Builder
	csv.WriteString("source,medium,campaign,utm_term,utm_content,giveaway_id,post_ref,code,cost_rub," +
		"clicks,signups,subscribed_total,still_subscribed,unsubscribed,retained_1d,retained_3d,retained_7d,retained_14d," +
		"retained_after_giveaway,cpa_dirty_rub,cpa_clean_rub,conversion_percent\n")
	for _, st := range stats {
		conv := 0.0
		if st.Clicks > 0 {
			conv = float64(st.Signups) / float64(st.Clicks) * 100
		}
		ret, _ := b.db.GetUTMLinkRetention(ctx, st.ID)
		cpaDirty, cpaClean := 0.0, 0.0
		if st.Cost > 0 && ret.Subscribed > 0 {
			cpaDirty = st.Cost / float64(ret.Subscribed)
		}
		if st.Cost > 0 && ret.StillIn > 0 {
			cpaClean = st.Cost / float64(ret.StillIn)
		}
		fmt.Fprintf(&csv, "%s,%s,%s,%s,%s,%d,%s,%s,%.2f,%d,%d,%d,%d,%d,%d,%d,%d,%d,%d,%.2f,%.2f,%.2f\n",
			csvEscape(st.Source), csvEscape(st.Medium), csvEscape(st.Campaign),
			csvEscape(st.UTMTerm), csvEscape(st.UTMContent), st.GiveawayID,
			csvEscape(st.PostRef), st.Code, st.Cost,
			st.Clicks, st.Signups, ret.Subscribed, ret.StillIn, ret.Subscribed-ret.StillIn,
			ret.Retained1d, ret.Retained3d, ret.Retained7d, ret.Retained14d,
			ret.RetainedAfterGiveaway, cpaDirty, cpaClean, conv)
	}

	// Загружаем CSV как файл и отправляем.
	filename := fmt.Sprintf("utm_stats_%d.csv", chID)
	data := []byte(csv.String())
	if err := b.api.SendFile(ctx, uid, filename, data); err != nil {
		// Fallback: если загрузка файла упала — шлём текстом (как раньше).
		b.log.Warn("CSV file upload failed, sending as text", "err", err, "user", uid)
		_, _ = b.api.SendTextWithKeyboard(ctx, uid,
			"📎 CSV-экспорт (файлом не удалось):\n\n"+csv.String(),
			util.ChannelUTMKeyboard(chID))
		return
	}
	_, _ = b.api.SendTextWithKeyboard(ctx, uid,
		"✅ CSV-файл отправлен выше. Открывается в Excel/Google Sheets.",
		util.ChannelUTMKeyboard(chID))
}

// csvEscape — quoting по RFC 4180 + нейтрализация формул Excel/LibreOffice
// (leading = + - @ или таб). Имена UTM-источников и профили участников —
// пользовательский ввод; без префикса '=HYPERLINK(...)' исполняется при
// открытии выгрузки организатором.
func csvEscape(s string) string {
	if strings.ContainsAny(s, ",\"\n\r") {
		s = "\"" + strings.ReplaceAll(s, "\"", "\"\"") + "\""
	}
	if len(s) > 0 && strings.ContainsRune("=+-@\t", rune(s[0])) {
		return "'" + s
	}
	return s
}

func ifEmpty(s, def string) string {
	if s == "" {
		return def
	}
	return s
}

// showChannelDynamics — динамика подписчиков канала по дням (органика + UTM).
func (b *Bot) showChannelDynamics(ctx context.Context, uid int64, p string) {
	idStr := strings.TrimPrefix(p, util.CBUTMDyn)
	chID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return
	}
	ch, err := b.db.GetChannel(ctx, chID, uid)
	if err != nil {
		_ = b.api.SendText(ctx, uid, "Канал не найден.")
		return
	}
	stats, err := b.db.ChannelDynamics(ctx, chID, 14)
	if err != nil {
		b.log.Warn("channel dynamics", "err", err, "channel", chID)
		_ = b.api.SendText(ctx, uid, "⚠️ Не удалось загрузить динамику. Попробуйте позже.")
		return
	}

	var totJ, totU int
	maxN := 1
	for _, d := range stats {
		totJ += d.Joins
		totU += d.Unsubs
		if d.Joins > maxN {
			maxN = d.Joins
		}
	}
	var sb strings.Builder
	title := ifEmpty(ch.Title, fmt.Sprintf("канал #%d", ch.ChatID))
	sb.WriteString(fmt.Sprintf("📈 Динамика «%s» (14 дней, МСК):\n\n", title))
	for _, d := range stats {
		bar := strings.Repeat("█", (d.Joins*10)/maxN)
		if bar == "" && d.Joins > 0 {
			bar = "▏"
		}
		sb.WriteString(fmt.Sprintf("  %s %s +%d", d.Day, bar, d.Joins))
		if d.Unsubs > 0 {
			sb.WriteString(fmt.Sprintf(" (−%d)", d.Unsubs))
		}
		sb.WriteString("\n")
	}
	sb.WriteString(fmt.Sprintf("\nИтого: +%d вступили, −%d отписались, итог %d", totJ, totU, totJ-totU))
	_, _ = b.api.SendTextWithKeyboard(ctx, uid, sb.String(), util.ChannelUTMKeyboard(chID))
}
