package handlers

import (
	"context"
	"fmt"
	"strconv"
	"strings"
	"time"

	"github.com/maxbot/giveawaybot/internal/store"
	"github.com/maxbot/giveawaybot/internal/util"
	"github.com/max-messenger/max-bot-api-client-go/v2/model"
)

const utmPayloadPrefix = "utm_" // payload диплинка: utm_<code>

// onUserAdded — юзер вступил в канал, где бот админ.
// Атрибуция UTM (если юзер пришёл по UTM-ссылке) + лог подписки.
func (b *Bot) onUserAdded(ctx context.Context, u model.Update) {
	userID := u.UserID
	chatID := u.ChatID
	inviterID := u.GetChat().InviterID
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
	_, attributed, err := b.db.UTMAccountSignup(ctx, userID, chatID)
	if err != nil {
		b.log.Debug("utm account signup", "err", err)
	}
	// Лог подписки (связь канал-юзер). Канал ищем по chat_id среди всех владельцев.
	source := "post"
	if attributed {
		source = "utm"
	}
	_ = inviterID // сохраняется ниже при необходимости
	_ = b.db.LogChannelJoin(ctx, userID, chatID, source)
}

// onUserRemoved — юзер покинул канал. Фиксируем отписку.
func (b *Bot) onUserRemoved(ctx context.Context, u model.Update) {
	userID := u.UserID
	chatID := u.ChatID
	if userID == 0 || chatID == 0 {
		return
	}
	_ = b.db.MarkUnsubscribedByChat(ctx, userID, chatID)
	_ = b.db.UTMAccountUnsub(ctx, userID, chatID)
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

// startCreateUTM — КОНСТРУКТОР трекинг-ссылки (P7): канал → название →
// пост-источник → стоимость. В отличие от быстрой ссылки (P6) добавляется
// выбор поста (куда вести: конкретный пост или канал) и стоимость для CPA.
func (b *Bot) startCreateUTM(ctx context.Context, uid int64, p string) {
	idStr := strings.TrimPrefix(p, util.CBUTMCreate)
	chID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return
	}
	// Канал должен принадлежать юзеру — crafted-callback с чужим channel_id
	// иначе привязал бы трекинг-ссылку к чужому каналу (см. showUTMMenu).
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
		"🎛 Конструктор трекинг-ссылки\n\n"+
			"Шаг 1/3. Отправьте НАЗВАНИЕ ссылки — как её искать в статистике.\n"+
			"Например: «Telegram», «Реклама у Иванова», «Пост в VK»",
		util.CancelKeyboard())
}

// handleUTMCreateInput — билдер трекинг-ссылки, три шага:
//  1. название источника (откуда идёт трафик в целом);
//  2. пост-источник (ссылка на конкретный пост/сторис);
//  3. стоимость выхода в ₽ (для CPA) — можно пропустить.
//
// Сессия payload: {channel_id, stage: "source"|"post"|"cost", source, post_ref}.
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
	chIDf2, _ := chIDf.(float64)
	chID := int64(chIDf2)
	stage, _ := sess.Payload["stage"].(string)

	// Шаг 3: стоимость выхода (₽) — для CPA; пустой ввод = пропустить.
	if stage == "cost" {
		costText := strings.TrimSpace(strings.Replace(text, ",", ".", 1))
		cost := 0.0
		if costText != "" {
			var err error
			cost, err = strconv.ParseFloat(costText, 64)
			if err != nil || cost < 0 || cost > 10000000 {
				_ = b.api.SendText(ctx, uid, "⚠️ Введите число (рубли, можно с копейками) или нажмите «⏭ Без стоимости».")
				return true
			}
		}
		source, _ := sess.Payload["source"].(string)
		postRef, _ := sess.Payload["post_ref"].(string)
		b.finishUTMLink(ctx, uid, chID, source, postRef, cost)
		return true
	}

	if stage != "post" {
		// Шаг «название ссылки» (общий у быстрой ссылки и конструктора).
		source := strings.TrimSpace(text)
		if err := util.ValidateUTMSource(source); err != nil {
			_ = b.api.SendText(ctx, uid, "⚠️ "+err.Error())
			return true
		}
		// Быстрая ссылка (P6): название — последний шаг, ссылка сразу.
		if quick, _ := sess.Payload["quick"].(bool); quick {
			b.finishUTMLink(ctx, uid, chID, source, "", 0)
			return true
		}
		if err := b.db.SetSessionPayload(ctx, uid, store.SessionUTMCreate, map[string]any{
			"channel_id": chIDf2, "stage": "post", "source": source,
		}); err != nil {
			b.log.Warn("utm: set session", "err", err, "user", uid)
			_ = b.api.SendText(ctx, uid, "⚠️ Не удалось начать создание ссылки. Попробуйте позже.")
			return true
		}
		_, _ = b.api.SendTextWithKeyboard(ctx, uid,
			"📍 Шаг 2/3. Пост-источник (необязательно).\n\n"+
				"Отправьте ссылку на конкретный пост (https://max.ru/…) — посетитель "+
				"придёт на него, а не на канал.\n"+
				"Или короткое описание размещения (напр. «Сторис», «Пост в TG от 15.10»). "+
				"В аналитике будет видно, какое размещение привело подписчиков.\n\n"+
				"Можно пропустить — тогда ссылка ведёт на сам канал.",
			util.PostRefKeyboard())
		return true
	}

	// Шаг 2: пост-источник (пустой ввод трактуем как пропуск) → шаг стоимости.
	source, _ := sess.Payload["source"].(string)
	postRef := strings.TrimSpace(text)
	if err := util.ValidatePostRef(postRef); err != nil {
		_ = b.api.SendText(ctx, uid, "⚠️ "+err.Error())
		return true
	}
	if err := b.db.SetSessionPayload(ctx, uid, store.SessionUTMCreate, map[string]any{
		"channel_id": chIDf2, "stage": "cost", "source": source, "post_ref": postRef,
	}); err != nil {
		b.log.Warn("utm: set session", "err", err, "user", uid)
	}
	_, _ = b.api.SendTextWithKeyboard(ctx, uid,
		"💰 Шаг 3/3. Стоимость выхода (необязательно).\n\n"+
			"Сколько заплачено за размещение этой ссылки (₽)? В аналитике посчитается "+
			"CPA — цена подписчика. Можно пропустить.",
		util.CostKeyboard())
	return true
}

// finishUTMLink — финал билдера: создаёт ссылку и показывает результат.
// ГЛАВНАЯ ссылка — startapp-диплинк мини-аппа (P6): весь трафик идёт через
// мини-апп, бот запоминает id кликнувшего (init_data) и ведёт его в
// канал/на пост — чат бота не открывается, юзер попадает в БД бота.
func (b *Bot) finishUTMLink(ctx context.Context, uid, chID int64, source, postRef string, cost float64) {
	code := util.RandomCode(10)
	if _, err := b.db.CreateUTMLink(ctx, uid, chID, source, "referral", source, postRef, code, cost); err != nil {
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
	// Основная ссылка: мини-апп считает клик С user_id → редирект в канал/пост.
	link := util.StartappLink(botName, "u_"+code)

	postLine := ""
	if postRef != "" {
		postLine = fmt.Sprintf("\n📍 Ведёт на: %s", postRef)
	}
	costLine := ""
	if cost > 0 {
		costLine = fmt.Sprintf("\n💰 Стоимость: %.2f ₽ (CPA посчитается в статистике)", cost)
	}

	// Быстрая ссылка без канала (легаси-сессии) — короткий ответ.
	if chID == 0 {
		_, _ = b.api.SendTextWithKeyboard(ctx, uid,
			fmt.Sprintf("✅ Трекинг-ссылка создана:\n\n%s\n\nНазвание: %s%s%s\n\n"+
				"Клик → мини-апп считает переход с id юзера → ведёт в канал/на пост.",
				link, source, postLine, costLine),
			util.TrackingLinkKeyboard(link, 0))
		return
	}

	title := ""
	if ch, err := b.db.GetChannel(ctx, chID, uid); err == nil {
		title = ifEmpty(ch.Title, fmt.Sprintf("канал #%d", ch.ChatID))
	}

	// Альтернативы: /r/<code> — мгновенный 302 без мини-аппа (клик анонимный,
	// домен бота виден); прямая ссылка на канал — без счётчика.
	alternatives := ""
	if b.publicURL != "" {
		alternatives += "\n• Короткая (клик анонимный, без мини-аппа): " + b.publicURL + "/r/" + code
	}
	if ch, err := b.db.GetChannel(ctx, chID, uid); err == nil {
		if direct := util.ChannelURL(ch.Link); direct != "" {
			alternatives += "\n• Прямая на канал (без счётчика): " + direct
		}
	}

	_, _ = b.api.SendTextWithKeyboard(ctx, uid,
		fmt.Sprintf("✅ Ссылка для «%s» готова:\n\n"+
			"⚡ %s\n\n"+
			"Клик → мини-апп запоминает id юзера → ведёт в канал/на пост. "+
			"В статистике видно, кто пришёл, кто подписался и через сколько отписался.\n\n"+
			"Название: %s%s%s%s",
			title, link, source, postLine, costLine, alternatives),
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
	sb.WriteString("📊 UTM-аналитика канала:\n\n")
	for _, s := range stats {
		conv := float64(0)
		if s.Clicks > 0 {
			conv = float64(s.Signups) / float64(s.Clicks) * 100
		}
		postRef := "—"
		if s.PostRef != "" {
			postRef = s.PostRef
		}
		fmt.Fprintf(&sb, "• %s / %s\n  пост: %s\n  клики: %d | подписки: %d | отписки: %d | конверсия: %.0f%%\n",
			s.Source, s.Campaign, postRef, s.Clicks, s.Signups, s.Unsubs, conv)
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
	csv.WriteString("source,medium,campaign,post_ref,code,cost_rub,cpa_rub,clicks,signups,unsubs,conversion_percent\n")
	for _, s := range stats {
		conv := 0.0
		if s.Clicks > 0 {
			conv = float64(s.Signups) / float64(s.Clicks) * 100
		}
		cpa := 0.0
		if s.Cost > 0 && s.Signups > 0 {
			cpa = s.Cost / float64(s.Signups)
		}
		fmt.Fprintf(&csv, "%s,%s,%s,%s,%s,%.2f,%.2f,%d,%d,%d,%.2f\n",
			csvEscape(s.Source), csvEscape(s.Medium), csvEscape(s.Campaign),
			csvEscape(s.PostRef), s.Code, s.Cost, cpa, s.Clicks, s.Signups, s.Unsubs, conv)
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

// createSimpleUTMLink — «⚡ Простая ссылка»: без опроса, source = «Простая <дата>».
// Считает пришло/ушло; UTM-детализация (источник/пост/стоимость) — в билдере.
func (b *Bot) createSimpleUTMLink(ctx context.Context, uid, chID int64) {
	if _, err := b.db.GetChannel(ctx, chID, uid); err != nil {
		_ = b.api.SendText(ctx, uid, "Канал не найден.")
		return
	}
	source := "Простая " + time.Now().In(util.MoscowLocation()).Format("02.01")
	b.finishUTMLink(ctx, uid, chID, source, "", 0)
}
