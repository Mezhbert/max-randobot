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
	// Диалог только что открылся ссылкой — без экрана юзер висел бы в пустом чате.
	b.sendStartScreen(ctx, userID)
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

	text := fmt.Sprintf("📊 %s\n\n", title) +
		fmt.Sprintf("Ссылок: %d\n", len(stats)) +
		fmt.Sprintf("Кликов: %d\n", totClicks) +
		fmt.Sprintf("Регистраций: %d\n", totSignups) +
		fmt.Sprintf("Отписок: %d\n", totUnsubs) +
		fmt.Sprintf("Конверсия: %.1f%%\n\n", conv) +
		"UTM-трекинг источников подписчиков."

	_, _ = b.api.SendTextWithKeyboard(ctx, uid, text, util.ChannelUTMKeyboard(chID))
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

// handleUTMCreateInput — ввод названия источника для UTM-ссылки.
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

	source := strings.TrimSpace(text)
	if err := util.ValidateUTMSource(source); err != nil {
		_ = b.api.SendText(ctx, uid, "⚠️ "+err.Error())
		return true
	}

	// Генерим ссылку: source — что ввёл юзер, остальное авто.
	code := util.RandomCode(10)
	if _, err := b.db.CreateUTMLink(ctx, uid, chID, source, "referral", source, "", code); err != nil {
		b.log.Warn("utm: create link", "err", err, "user", uid, "channel", chID)
		_ = b.api.SendText(ctx, uid, "⚠️ Не удалось создать ссылку. Попробуйте позже.")
		return true
	}
	_ = b.db.ClearSession(ctx, uid)

	botName := b.botUsername
	if botName == "" {
		b.log.Warn("botUsername не задан — UTM-диплинк будет нерабочим", "user", uid)
		botName = "ВАШ_БОТ"
	}
	link := fmt.Sprintf("https://max.ru/%s/start/%s%s", botName, utmPayloadPrefix, code)

	// Быстрая ссылка (без канала) — свой ответ и своя клавиатура.
	if chID == 0 {
		_, _ = b.api.SendTextWithKeyboard(ctx, uid,
			fmt.Sprintf("✅ Трекинг-ссылка создана:\n\n%s\n\n"+
				"Источник: %s\n\n"+
				"Размещайте где угодно — клики и подписки будут учтены "+
				"в «Аналитика → Каналы».", link, source),
			util.BackKeyboard())
		return true
	}
	_, _ = b.api.SendTextWithKeyboard(ctx, uid,
		fmt.Sprintf("✅ Трекинг-ссылка создана:\n\n%s\n\n"+
			"Источник: %s\n\n"+
			"Размещайте эту ссылку где угодно — каждый переход и подписка будут "+
			"учтены в аналитике этого канала.",
			link, source),
		util.ChannelUTMKeyboard(chID))
	return true
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
		fmt.Fprintf(&sb, "• %s / %s\n  клики: %d | подписки: %d | отписки: %d | конверсия: %.0f%%\n",
			s.Source, s.Campaign, s.Clicks, s.Signups, s.Unsubs, conv)
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
	csv.WriteString("source,medium,campaign,post_ref,code,clicks,signups,unsubs,conversion_percent\n")
	for _, s := range stats {
		conv := 0.0
		if s.Clicks > 0 {
			conv = float64(s.Signups) / float64(s.Clicks) * 100
		}
		fmt.Fprintf(&csv, "%s,%s,%s,%s,%s,%d,%d,%d,%.2f\n",
			csvEscape(s.Source), csvEscape(s.Medium), csvEscape(s.Campaign),
			csvEscape(s.PostRef), s.Code, s.Clicks, s.Signups, s.Unsubs, conv)
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
