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

// startQuickUTM — быстрая трекинг-ссылка БЕЗ привязки к каналу (из меню
// «Аналитика»). Юзер вводит только название источника — ссылка готова.
func (b *Bot) startQuickUTM(ctx context.Context, uid int64) {
	if err := b.db.SetSessionPayload(ctx, uid, store.SessionUTMCreate, map[string]any{"channel_id": float64(0)}); err != nil {
		_ = b.api.SendText(ctx, uid, "⚠️ "+err.Error())
		return
	}
	_, _ = b.api.SendTextWithKeyboard(ctx, uid,
		"🔗 Создание трекинг-ссылки\n\n"+
			"Отправьте название источника — откуда будут приходить подписчики.\n"+
			"Например: «Telegram», «Реклама у Иванова», «Пост в VK»\n\n"+
			"Бот сгенерирует уникальную ссылку с трекингом автоматически.",
		util.CancelKeyboard())
}

// startCreateUTM — начало создания UTM-ссылки. Юзер вводит только название
// источника (одно слово/фразу) — остальное бот генерит автоматически.
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
		"🔗 Создание трекинг-ссылки\n\n"+
			"Отправьте название источника — откуда будут приходить подписчики.\n"+
			"Например: «Telegram», «Реклама у Иванова», «Пост в VK»\n\n"+
			"Бот сгенерирует уникальную ссылку с трекингом автоматически.",
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
		// Шаг 1: название источника.
		source := strings.TrimSpace(text)
		if err := util.ValidateUTMSource(source); err != nil {
			_ = b.api.SendText(ctx, uid, "⚠️ "+err.Error())
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
			"📍 Пост-источник (необязательно).\n\n"+
				"Где будет размещена ссылка? Отправьте ссылку на конкретный пост "+
				"или короткое описание (напр. «Пост в TG от 15.10», «Сторис»). "+
				"В аналитике будет видно, какой пост привёл каждого подписчика.\n\n"+
				"Можно пропустить — тогда привязки к посту не будет.",
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
		"💰 Стоимость выхода (необязательно).\n\n"+
			"Сколько заплачено за размещение этой ссылки (₽)? В аналитике посчитается "+
			"CPA — цена подписчика. Можно пропустить.",
		util.CostKeyboard())
	return true
}

// finishUTMLink — финал билдера: создаёт ссылку и показывает результат.
// Вызывается из шага «пост» и по кнопке «⏭ Без поста-источника».
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
	// Бесшовная ссылка: открывает мини-апп (без чата бота!), сервер
	// идентифицирует юзера по init_data и ведёт его в канал/пост.
	link := fmt.Sprintf("https://max.ru/%s?startapp=u_%s", botName, code)

	postLine := ""
	if postRef != "" {
		postLine = fmt.Sprintf("\n📍 Пост-источник: %s", postRef)
	}

	// Быстрая ссылка (без канала) — свой ответ и своя клавиатура.
	if chID == 0 {
		_, _ = b.api.SendTextWithKeyboard(ctx, uid,
			fmt.Sprintf("✅ Трекинг-ссылка создана:\n\n%s\n\nИсточник: %s%s\n\n"+
				"Размещайте где угодно — клики и подписки будут учтены "+
				"в «Аналитика → Каналы».", link, source, postLine),
			util.BackKeyboard())
		return
	}

	// Канальная ссылка: главный вариант — БЕСШОВНАЯ /r/<code> на нашем домене
	// (клик считается на редиректе, юзер сразу в канале/на посте). Прямая —
	// для случаев, когда домен бота светить не хочется. Диплинк — для точной
	// атрибуции «кто именно подписался».
	title, directURL := "", ""
	if ch, err := b.db.GetChannel(ctx, chID, uid); err == nil {
		title = ifEmpty(ch.Title, fmt.Sprintf("канал #%d", ch.ChatID))
		directURL = util.ChannelURL(ch.Link)
	}

	mainLink := link // диплинк-фолбэк (если публичный домен бота неизвестен)
	if b.publicURL != "" {
		mainLink = b.publicURL + "/r/" + code
	}
	notes := "Клик → мгновенный переход в канал/на пост, клики считаются."
	if postURL := strings.TrimSpace(postRef); util.IsHTTPURL(postURL) {
		notes = "Клик → мгновенный переход на конкретный пост, клики считаются."
	}

	_, _ = b.api.SendTextWithKeyboard(ctx, uid,
		fmt.Sprintf("✅ Ссылка для «%s» готова:\n\n"+
			"⚡ %s\n%s\n\n"+
			"Альтернативы:\n"+
			"• Прямая на канал (домен бота не светится, клики НЕ считаются): %s\n"+
			"• С точной атрибуцией подписок (кто именно вступил): %s\n\n"+
			"Источник: %s%s",
			title, mainLink, notes, ifEmpty(directURL, "—"), link, source, postLine),
		util.ChannelUTMKeyboard(chID))
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
