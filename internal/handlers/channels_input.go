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
	_ = b.api.SendTextWithKeyboard(ctx, uid,
		"➕ Добавление канала.\n\n"+
			"Бот должен быть администратором в канале. Отправьте:\n"+
			"• ID канала (число), или\n"+
			"• перешлите любое сообщение из этого канала.\n\n"+
			"Узнать ID канала: добавьте бота админом и перешлите сообщение.",
		util.ChannelAddKeyboard())
}

// handleAddChannelInput — ввод пользователя в режиме добавления канала.
// Возвращает true, если сообщение было обработано (канал добавлен/ошибка).
func (b *Bot) handleAddChannelInput(ctx context.Context, uid int64, msg model.MessageUpdate) (handled bool) {
	var chatID int64

	// 1) Пересланное сообщение из канала → берём chat_id из forward.link.chat_id.
	if msg.Link != nil && msg.Link.ChatID != 0 {
		chatID = msg.Link.ChatID
	} else {
		// 2) Числовой ID.
		text := strings.TrimSpace(msg.Body.Text)
		n, err := strconv.ParseInt(text, 10, 64)
		if err != nil {
			_ = b.api.SendText(ctx, uid, "⚠️ Не похоже на ID канала. Отправьте число или перешлите сообщение из канала.")
			return true
		}
		chatID = n
	}

	// Проверяем админство и получаем инфу о чате.
	info, isAdmin, err := b.api.CheckChannelAdmin(ctx, chatID)
	if err != nil {
		_ = b.api.SendText(ctx, uid, "⚠️ Не удалось получить доступ к каналу. Убедитесь, что бот добавлен админом.\n("+err.Error()+")")
		return true
	}
	if !isAdmin {
		_ = b.api.SendTextWithKeyboard(ctx, uid,
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
	_ = b.api.SendTextWithKeyboard(ctx, uid,
		fmt.Sprintf("✅ Канал «%s» добавлен! Теперь вы можете использовать его в розыгрышах и генерировать UTM-ссылки.", title),
		util.BackKeyboard())
	return true
}

// toggleChannelSelection — тогглинг канала в выборе на шагах FSM sub/pub channels.
func (b *Bot) toggleChannelSelection(ctx context.Context, uid int64, p string) {
	// p = "ch_toggle_sub:<channelInternalID>" или "...pub:..."
	isSub := strings.HasPrefix(p, "ch_toggle_sub:")
	field := "subscribe_channel_ids"
	if !isSub {
		field = "publish_channel_ids"
	}
	idStr := strings.TrimPrefix(strings.TrimPrefix(p, "ch_toggle_sub:"), "ch_toggle_pub:")
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
	var updated []int64
	found := false
	for _, id := range current {
		if id == chID {
			found = true
			continue
		}
		updated = append(updated, id)
	}
	if !found {
		updated = append(updated, chID)
	}

	if err := b.db.UpdateGiveawayField(ctx, g.ID, field, updated); err != nil {
		_ = b.api.SendText(ctx, uid, "⚠️ Ошибка обновления: "+err.Error())
		return
	}

	// Перерисовываем шаг с актуальными галочками.
	gg, _ := b.db.GetGiveawayAny(ctx, g.ID)
	b.showStep(ctx, uid, gg, gg.DraftStep)
}
