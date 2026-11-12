package handlers

import (
	"context"
	"fmt"
	"strconv"
	"strings"

	"github.com/maxbot/giveawaybot/internal/fsm"
	"github.com/maxbot/giveawaybot/internal/store"
	"github.com/max-messenger/max-bot-api-client-go/v2/model"
)

// startAddChannel — экран подключения канала (мануал §5): картинка со схемой
// прав, инструкция, «🔄 Проверить». Если идёт создание розыгрыша — после
// подключения вернёмся к текущему шагу (channel/subs).
func (b *Bot) startAddChannel(ctx context.Context, uid int64) {
	returnStep := ""
	if g, err := b.db.GetDraftGiveaway(ctx, uid); err == nil && g.DraftStep != "" {
		if g.DraftStep == fsm.StepChannel || g.DraftStep == fsm.StepSubs {
			returnStep = g.DraftStep
		}
	}
	b.showConnectChannelScreen(ctx, uid, returnStep)
}

// refreshChannels — 🔄 обновляет статус админства бота во ВСЕХ каналах юзера.
// Безопасно: обходятся только каналы уже добавленные пользователем (никакого
// перебора чужих каналов — утечка админства в чужих каналах невозможна).
// Каналы, где бот больше НЕ админ, удаляются из списка: юзер не имеет к ним
// доступа (сценарий: другой админ убрал бота — розыгрыши там невозможны).
// Передобавление — классически, пересылкой поста.
func (b *Bot) refreshChannels(ctx context.Context, uid int64) {
	channels, err := b.db.ListChannels(ctx, uid)
	if err != nil || len(channels) == 0 {
		b.showChannels(ctx, uid)
		return
	}

	updated, removed := 0, 0
	for _, c := range channels {
		isAdmin, err := b.api.IsBotAdminInChat(ctx, c.ChatID)
		if err != nil {
			// Ошибка проверки (бот удалён из канала и т.п.) — считаем не админом.
			isAdmin = false
		}
		switch {
		case isAdmin && !c.IsBotAdmin:
			if err := b.db.SetChannelAdminFlag(ctx, c.ID, true); err == nil {
				updated++
			}
		case !isAdmin:
			// Бот потерял админство — канал больше не юзабелен, удаляем.
			if err := b.db.DeleteChannel(ctx, c.ID, uid); err == nil {
				removed++
			}
		}
	}

	msg := "🔄 Каналы обновлены."
	if updated > 0 {
		msg += fmt.Sprintf("\n✅ Админство вернулось: %d.", updated)
	}
	if removed > 0 {
		msg += fmt.Sprintf("\n🗑 Удалено (бот больше не админ): %d.\nПередобавить — пересылкой поста из канала.", removed)
	}
	_ = b.api.SendText(ctx, uid, msg)
	b.showChannels(ctx, uid)
}

// handleAddChannelInput — ввод пользователя в режиме добавления канала
// (ссылка/ID/пересылка). Возвращает true, если сообщение было обработано.
func (b *Bot) handleAddChannelInput(ctx context.Context, uid int64, msg model.MessageUpdate) (handled bool) {
	// Помним шаг возврата (если подключали из мастера).
	sess, _ := b.db.GetSessionPayload(ctx, uid)
	returnStep, _ := sess.Payload["return_step"].(string)

	var chatID int64

	// 1) Пересланное сообщение из канала → берём chat_id из forward.link.chat_id.
	if msg.Link != nil && msg.Link.ChatID != 0 {
		chatID = msg.Link.ChatID
	} else {
		text := strings.TrimSpace(msg.Body.Text)
		// 2) Ссылка на канал (@username / https://...) — резолвим через
		// getChatByLink (обёртки в SDK нет — сырой REST).
		if isChannelLink(text) {
			chat, err := b.api.GetChatByLink(ctx, text)
			if err != nil {
				_ = b.api.SendText(ctx, uid, "⚠️ Канал по ссылке не найден. Проверьте ссылку (канал должен быть публичным).")
				return true
			}
			chatID = chat.ChatID
		} else {
			// 3) Числовой ID.
			n, err := strconv.ParseInt(text, 10, 64)
			if err != nil {
				_ = b.api.SendText(ctx, uid, "⚠️ Не похоже на канал. Отправьте ссылку, ID канала или перешлите сообщение из него.")
				return true
			}
			chatID = n
		}
	}

	// Проверяем админство и получаем инфу о чате.
	// NOTE: в MAX chat_id каналов ОТРИЦАТЕЛЬНЫЕ (напр. -74970071115840,
	// см. логи публикации), положительные — юзеры. Проверок на знак быть
	// не должно: валидный id из форварда/ссылки всегда «ненулевой».
	if chatID == 0 {
		_ = b.api.SendText(ctx, uid, "⚠️ Не похоже на канал. Отправьте ссылку, ID канала или перешлите сообщение из него.")
		return true
	}
	info, isAdmin, err := b.api.CheckChannelAdmin(ctx, chatID)
	if err != nil {
		b.log.Warn("add channel: check admin", "err", err, "chat", chatID, "user", uid)
		_ = b.api.SendText(ctx, uid, "⚠️ Не удалось получить доступ к каналу. Убедитесь, что бот добавлен админом, и повторите ещё раз.")
		return true
	}
	if !isAdmin {
		b.showConnectChannelScreen(ctx, uid, returnStep)
		return true
	}

	// Сохраняем канал.
	c, err := b.db.AddChannel(ctx, store.Channel{
		OwnerUserID: uid,
		ChatID:      chatID,
		Title:       info.Title,
		Link:        info.Link,
		IsPublic:    info.IsPublic,
		IsBotAdmin:  true,
	})
	if err != nil {
		// Чаще всего — канал уже добавлен (UNIQUE owner+chat). Без текста
		// ошибки: имя констрейнта — внутренняя деталь.
		b.log.Warn("add channel: save", "err", err, "chat", chatID, "user", uid)
		_ = b.api.SendText(ctx, uid, "⚠️ Не удалось сохранить канал (возможно, он уже добавлен).")
		return true
	}

	_ = b.db.ClearSession(ctx, uid)
	title := c.Title
	if title == "" {
		title = fmt.Sprintf("канал #%d", c.ChatID)
	}
	_ = b.api.SendText(ctx, uid, "✅ Канал «"+title+"» подключён!")

	// Возвращаемся к шагу мастера, если подключали из него.
	if returnStep == fsm.StepChannel || returnStep == fsm.StepSubs {
		if g, gerr := b.db.GetDraftGiveaway(ctx, uid); gerr == nil {
			step := fsm.NormalizeStep(returnStep)
			_ = b.db.SetDraftStep(ctx, g.ID, step)
			if step == fsm.StepChannel {
				b.selectPublishChannel(ctx, g.ID, c.ID)
			}
			gg, _ := b.db.GetGiveawayAny(ctx, g.ID)
			_ = b.db.UpdateGiveawayField(ctx, g.ID, "channel_select_mid", nil)
			b.showStep(ctx, uid, gg, step)
			return true
		}
	}
	b.showChannels(ctx, uid)
	return true
}

// isChannelLink — похоже ли на публичную ссылку канала (@name, t.me/, max.ru/).
func isChannelLink(s string) bool {
	if strings.HasPrefix(s, "@") && len(s) > 1 {
		return true
	}
	for _, p := range []string{"https://", "http://", "max.ru/", "t.me/"} {
		if strings.HasPrefix(s, p) {
			return strings.Contains(s, "/") // точно ссылка, не просто число
		}
	}
	return false
}
