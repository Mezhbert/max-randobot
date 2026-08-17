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

// startAddChannel — переводит юзера в режим добавления канала.
func (b *Bot) startAddChannel(ctx context.Context, uid int64) {
	if err := b.db.SetSession(ctx, uid, store.SessionAddingChannel); err != nil {
		_ = b.api.SendText(ctx, uid, "⚠️ Ошибка: "+err.Error())
		return
	}
	_, _ = b.api.SendTextWithKeyboard(ctx, uid,
		"➕ Добавление канала.\n\n"+
			"Бот должен быть администратором в канале. Отправьте:\n"+
			"• ссылку на канал (например @channel или https://max.ru/channel), или\n"+
			"• ID канала (число), или\n"+
			"• перешлите любое сообщение из этого канала.\n\n"+
			"Не вы админ? Попросите админа добавить бота, затем перешлите сообщение из канала.",
		util.ChannelAddKeyboard())
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

// handleAddChannelInput — ввод пользователя в режиме добавления канала.
// Возвращает true, если сообщение было обработано (канал добавлен/ошибка).
func (b *Bot) handleAddChannelInput(ctx context.Context, uid int64, msg model.MessageUpdate) (handled bool) {
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
	info, isAdmin, err := b.api.CheckChannelAdmin(ctx, chatID)
	if err != nil {
		_ = b.api.SendText(ctx, uid, "⚠️ Не удалось получить доступ к каналу. Убедитесь, что бот добавлен админом.\n("+err.Error()+")")
		return true
	}
	if !isAdmin {
		_, _ = b.api.SendTextWithKeyboard(ctx, uid,
			"⚠️ Бот НЕ является администратором в этом канале. Добавьте бота админом и повторите.",
			util.ChannelAddKeyboard())
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
		_ = b.api.SendText(ctx, uid, "⚠️ Ошибка сохранения канала: "+err.Error())
		return true
	}

	_ = b.db.ClearSession(ctx, uid)
	title := c.Title
	if title == "" {
		title = fmt.Sprintf("канал #%d", c.ChatID)
	}
	_, _ = b.api.SendTextWithKeyboard(ctx, uid,
		fmt.Sprintf("✅ Канал «%s» добавлен! Теперь вы можете использовать его в розыгрышах и генерировать UTM-ссылки.", title),
		util.BackKeyboard())
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

// toggleChannelSelection — тогглинг канала в выборе на шагах FSM sub/pub channels.
// Блокирует выбор не-админ каналов (алерт) — использовать их в розыгрыше нельзя.
func (b *Bot) toggleChannelSelection(ctx context.Context, uid int64, p string) {
	// p = "ch_toggle_sub:<channelInternalID>" или "...pub:..."
	isSub := strings.HasPrefix(p, util.CBChannelToggleSub)
	field := "subscribe_channel_ids"
	if !isSub {
		field = "publish_channel_ids"
	}
	idStr := strings.TrimPrefix(strings.TrimPrefix(p, util.CBChannelToggleSub), util.CBChannelTogglePub)
	chID, err := strconv.ParseInt(idStr, 10, 64)
	if err != nil {
		return
	}

	g, err := b.db.GetDraftGiveaway(ctx, uid)
	if err != nil {
		_ = b.api.SendText(ctx, uid, "Создание не активно.")
		return
	}

	// Тогглим ID в массиве.
	current := g.SubscribeChannelIDs
	if !isSub {
		current = g.PublishChannelIDs
	}
	found := false
	var updated []int64
	for _, id := range current {
		if id == chID {
			found = true
			continue
		}
		updated = append(updated, id)
	}
	if !found {
		// Выбор канала: проверяем админство бота — не-админ нельзя выбрать.
		if ch, err := b.db.GetChannel(ctx, chID, uid); err == nil && !ch.IsBotAdmin {
			title := ch.Title
			if title == "" {
				title = fmt.Sprintf("канал #%d", ch.ChatID)
			}
			_ = b.api.SendText(ctx, uid, "⚠️ Бот не админ в «"+title+"» — канал нельзя выбрать. Обновите список: «Мои каналы → 🔄».")
			return
		}
		updated = append(updated, chID)
	}

	if err := b.db.UpdateGiveawayField(ctx, g.ID, field, updated); err != nil {
		_, _ = b.api.SendTextWithKeyboard(ctx, uid, "⚠️ Ошибка обновления: "+err.Error(), nil)
		return
	}

	// Перерисовываем шаг edit-in-place (редактируем то же сообщение, не шлём новое).
	gg, _ := b.db.GetGiveawayAny(ctx, g.ID)
	text, kb := buildChannelSelectKB(ctx, b, uid, gg, gg.DraftStep)
	if gg.ChannelSelectMID != "" {
		if err := b.api.EditChatText(ctx, gg.ChannelSelectMID, text, []model.Attachment{kb.Build()}, ""); err != nil {
			// Edit упал — шлём новым сообщением и обновляем mid.
			mid, _ := b.api.SendTextWithKeyboard(ctx, uid, text, kb)
			if mid != "" {
				_ = b.db.UpdateGiveawayField(ctx, g.ID, "channel_select_mid", mid)
			}
		}
	} else {
		// mid не запомнен (старая сессия) — шлём новое и запоминаем.
		mid, _ := b.api.SendTextWithKeyboard(ctx, uid, text, kb)
		if mid != "" {
			_ = b.db.UpdateGiveawayField(ctx, g.ID, "channel_select_mid", mid)
		}
	}
}
