package handlers

import (
	"context"
	"fmt"

	"github.com/maxbot/giveawaybot/internal/store"
	"github.com/maxbot/giveawaybot/internal/util"
	"github.com/max-messenger/max-bot-api-client-go/v2/model"
)

// showGiveawayFilters — фильтры по типам розыгрышей.
func (b *Bot) showGiveawayFilters(ctx context.Context, uid int64) {
	kb := model.NewKeyboard()
	kb.AddRow().
		AddCallBack("Текущие", util.CBGiveawayFilter+store.StatusActive).
		AddCallBack("Запланированные", util.CBGiveawayFilter+store.StatusScheduled)
	kb.AddRow().
		AddCallBack("В процессе создания", util.CBGiveawayFilter+store.StatusDraft).
		AddCallBack("Завершённые", util.CBGiveawayFilter+store.StatusFinished)
	kb.AddRow().AddCallBack("🏠 В меню", util.CBMainMenu)
	_ = b.api.SendTextWithKeyboard(ctx, uid, "📋 Мои розыгрыши. Выберите тип:", kb)
}

// showGiveawaysByStatus — список розыгрышей выбранного типа.
func (b *Bot) showGiveawaysByStatus(ctx context.Context, uid int64, status string) {
	items, err := b.db.ListGiveaways(ctx, uid, status)
	if err != nil || len(items) == 0 {
		_ = b.api.SendTextWithKeyboard(ctx, uid, "Здесь пока пусто.", util.BackKeyboard())
		return
	}
	kb := model.NewKeyboard()
	for _, g := range items {
		title := g.Name
		if title == "" {
			title = "(без названия)"
		}
		kb.AddRow().AddCallBack(title, util.CBGwOpen+fmt.Sprintf("%d", g.ID))
	}
	kb.AddRow().AddCallBack("← Назад", util.CBGwList)
	_ = b.api.SendTextWithKeyboard(ctx, uid, "Выберите розыгрыш:", kb)
}

// showGiveawayDetails — карточка розыгрыша с действиями.
func (b *Bot) showGiveawayDetails(ctx context.Context, uid, id int64) {
	g, err := b.db.GetGiveaway(ctx, id, uid)
	if err != nil {
		_ = b.api.SendText(ctx, uid, "Розыгрыш не найден.")
		return
	}
	text := fmt.Sprintf("🎁 %s\nСтатус: %s\n", nonEmpty(g.Name), g.Status)
	if g.PublishAt != nil {
		text += fmt.Sprintf("Старт: %s\n", util.FormatMoscowTime(*g.PublishAt))
	}
	if g.FinishAt != nil {
		text += fmt.Sprintf("Итоги: %s\n", util.FormatMoscowTime(*g.FinishAt))
	}
	text += fmt.Sprintf("Победителей: %d\n", g.WinnersCount)

	kb := model.NewKeyboard()
	switch g.Status {
	case store.StatusDraft:
		// черновик: продолжить создание / удалить
		kb.AddRow().AddCallBack("Продолжить создание", util.CBGwEdit+fmt.Sprintf("%d", g.ID))
		kb.AddRow().AddCallBack("🗑 Удалить черновик", util.CBGwDelete+fmt.Sprintf("%d", g.ID))
	case store.StatusScheduled:
		kb.AddRow().AddCallBack("Отменить", util.CBGwCancel+fmt.Sprintf("%d", g.ID))
	case store.StatusActive:
		// текущие: редактирование в полной версии
		kb.AddRow().AddCallBack("✏ Редактировать", util.CBGwEdit+fmt.Sprintf("%d", g.ID))
	}
	kb.AddRow().AddCallBack("← Назад", util.CBGwList)
	_ = b.api.SendTextWithKeyboard(ctx, uid, text, kb)
}

// showChannels — экран "мои каналы".
func (b *Bot) showChannels(ctx context.Context, uid int64) {
	items, err := b.db.ListChannels(ctx, uid)
	if err != nil {
		_ = b.api.SendText(ctx, uid, "Ошибка загрузки каналов.")
		return
	}
	var text string
	kb := model.NewKeyboard()
	if len(items) == 0 {
		text = "📢 Мои каналы пусты.\n\nЧтобы добавить канал:\n" +
			"1) Добавьте бота администратором в канал.\n" +
			"2) Отправьте ID канала или перешлите сообщение из него."
	} else {
		text = "📢 Мои каналы. Нажмите на канал для деталей/UTM-аналитики:\n\n"
		for _, c := range items {
			admin := "✅"
			if !c.IsBotAdmin {
				admin = "⚠️"
			}
			title := c.Title
			if title == "" {
				title = fmt.Sprintf("канал #%d", c.ChatID)
			}
			// Каждая строка — кнопка-канал (вход в детали/UTM).
			kb.AddRow().AddCallBack(admin+" "+title, util.CBChannelUTM+fmt.Sprintf("%d", c.ID))
		}
	}
	kb.AddRow().AddCallBack("➕ Добавить канал", util.CBChannelAdd)
	kb.AddRow().AddCallBack("🏠 В меню", util.CBMainMenu)
	_ = b.api.SendTextWithKeyboard(ctx, uid, text, kb)
}

// showChannelListForAnalytics — список каналов для просмотра аналитики (alias).
func (b *Bot) showChannelListForAnalytics(ctx context.Context, uid int64) {
	b.showChannels(ctx, uid)
}

// showMyParticipations — экран "мои участия".
func (b *Bot) showMyParticipations(ctx context.Context, uid int64) {
	items, err := b.db.ListMyParticipations(ctx, uid)
	if err != nil || len(items) == 0 {
		_ = b.api.SendTextWithKeyboard(ctx, uid, "Вы пока ни в чём не участвуете.", util.BackKeyboard())
		return
	}
	text := "🎟 Ваши участия:\n\n"
	for _, g := range items {
		title := g.Name
		if title == "" {
			title = "(без названия)"
		}
		text += fmt.Sprintf("• %s — статус %s\n", title, g.Status)
	}
	_ = b.api.SendTextWithKeyboard(ctx, uid, text, util.BackKeyboard())
}

func nonEmpty(s string) string {
	if s == "" {
		return "(без названия)"
	}
	return s
}
