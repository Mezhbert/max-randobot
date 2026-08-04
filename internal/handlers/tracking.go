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

// onBotStartedPayload — обработка payload диплинка из bot_started.
// payload приходит в формате "utm_<code>". Если есть — фиксируем клик + атрибуцию.
func (b *Bot) onBotStartedPayload(ctx context.Context, userID int64, payload string) {
	if payload == "" {
		return
	}
	code := strings.TrimPrefix(payload, utmPayloadPrefix)
	if code == "" || code == payload {
		return // не UTM-диплинк
	}
	link, err := b.db.GetUTMLinkByCode(ctx, code)
	if err != nil {
		b.log.Debug("utm link not found", "code", code, "err", err)
		return
	}
	if err := b.db.UTMRecordClick(ctx, link.ID, userID); err != nil {
		b.log.Warn("utm record click", "err", err)
	}
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
	admin := "✅ бот админ"
	if !ch.IsBotAdmin {
		admin = "⚠️ бот НЕ админ (трекинг подписок невозможен)"
	}
	_ = b.api.SendTextWithKeyboard(ctx, uid,
		fmt.Sprintf("📢 %s\n%s\n\nUTM-трекинг источников подписок.", title, admin),
		util.ChannelUTMKeyboard(chID))
}

// startCreateUTM — начало создания UTM-ссылки (переход в режим ввода).
func (b *Bot) startCreateUTM(ctx context.Context, uid int64, p string) {
	idStr := strings.TrimPrefix(p, util.CBUTMCreate)
	chID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return
	}
	if err := b.db.SetSessionPayload(ctx, uid, store.SessionUTMCreate, map[string]any{"channel_id": float64(chID)}); err != nil {
		_ = b.api.SendText(ctx, uid, "⚠️ "+err.Error())
		return
	}
	_ = b.api.SendTextWithKeyboard(ctx, uid,
		"🔗 Создание UTM-ссылки.\n\nОтправьте одним сообщением в формате:\n"+
			"источник | кампания | пост\n\n"+
			"Пример:\n"+
			"telegram | summer_giveaway | t.me/me/123\n\n"+
			"Поле «пост» можно оставить пустым.",
		util.CancelKeyboard())
}

// handleUTMCreateInput — ввод данных UTM-ссылки (режим SessionUTMCreate).
func (b *Bot) handleUTMCreateInput(ctx context.Context, uid int64, text string) bool {
	sess, err := b.db.GetSessionPayload(ctx, uid)
	if err != nil || sess.State != store.SessionUTMCreate {
		_ = b.db.ClearSession(ctx, uid)
		return false
	}
	chIDf, ok := sess.Payload["channel_id"]
	if !ok {
		_ = b.db.ClearSession(ctx, uid)
		_ = b.api.SendText(ctx, uid, "Канал утерян, начните заново.")
		return true
	}
	chID := int64(chIDf.(float64))

	parts := strings.Split(text, "|")
	source := strings.TrimSpace(parts[0])
	campaign := ""
	postRef := ""
	if len(parts) > 1 {
		campaign = strings.TrimSpace(parts[1])
	}
	if len(parts) > 2 {
		postRef = strings.TrimSpace(parts[2])
	}
	if source == "" {
		_ = b.api.SendText(ctx, uid, "⚠️ Источник обязателен. Отправьте: источник | кампания | пост")
		return true
	}

	code := util.RandomCode(10)
	if _, err := b.db.CreateUTMLink(ctx, uid, chID, source, "post", campaign, postRef, code); err != nil {
		_ = b.api.SendText(ctx, uid, "⚠️ Ошибка: "+err.Error())
		return true
	}
	_ = b.db.ClearSession(ctx, uid)

	botName := b.botUsername
	if botName == "" {
		botName = "ВАШ_БОТ"
	}
	link := fmt.Sprintf("https://max.ru/%s/start/%s%s", botName, utmPayloadPrefix, code)
	_ = b.api.SendTextWithKeyboard(ctx, uid,
		fmt.Sprintf("✅ UTM-ссылка создана:\n\n%s\n\n"+
			"Источник: %s\nКампания: %s\nПост: %s\n\n"+
			"Когда пользователь перейдёт по ссылке и стартанёт бота — мы зафиксируем клик. "+
			"Если он подпишется на канал (бот админ) — припишем подписку к этой ссылке.",
			link, source, campaign, ifEmpty(postRef, "—")),
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
		_ = b.api.SendTextWithKeyboard(ctx, uid, "📊 UTM-ссылок пока нет. Создайте первую.", util.ChannelUTMKeyboard(chID))
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
	_ = b.api.SendTextWithKeyboard(ctx, uid, sb.String(), util.ChannelUTMStatsKeyboard(chID))
}

// exportUTMCSV — экспорт статистики UTM в CSV (текстом).
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
	_ = b.api.SendTextWithKeyboard(ctx, uid,
		"📎 CSV-экспорт UTM-аналитики:\n\n"+csv.String(),
		util.ChannelUTMKeyboard(chID))
}

func csvEscape(s string) string {
	if strings.ContainsAny(s, ",\"\n") {
		return "\"" + strings.ReplaceAll(s, "\"", "\"\"") + "\""
	}
	return s
}

func ifEmpty(s, def string) string {
	if s == "" {
		return def
	}
	return s
}
