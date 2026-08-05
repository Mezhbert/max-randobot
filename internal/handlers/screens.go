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
	_, _ = b.api.SendTextWithKeyboard(ctx, uid, "📋 Мои розыгрыши. Выберите тип:", kb)
}

// showGiveawaysByStatus — список розыгрышей выбранного типа.
// Для черновиков (draft) показывает только именованные (сохранённые), без мусора.
func (b *Bot) showGiveawaysByStatus(ctx context.Context, uid int64, status string) {
	items, err := b.db.ListGiveaways(ctx, uid, status)
	if err != nil {
		_, _ = b.api.SendTextWithKeyboard(ctx, uid, "Здесь пока пусто.", util.BackKeyboard())
		return
	}
	// Для черновиков фильтруем пустые (без названия) — это мусор от прерванных попыток.
	var filtered []store.Giveaway
	for _, g := range items {
		if status == store.StatusDraft && g.Name == "" {
			continue
		}
		filtered = append(filtered, g)
	}
	if len(filtered) == 0 {
		_, _ = b.api.SendTextWithKeyboard(ctx, uid, "Здесь пока пусто.", util.BackKeyboard())
		return
	}
	kb := model.NewKeyboard()
	for _, g := range filtered {
		title := g.Name
		if title == "" {
			title = "(без названия)"
		}
		kb.AddRow().AddCallBack(title, util.CBGwOpen+fmt.Sprintf("%d", g.ID))
	}
	kb.AddRow().AddCallBack("← Назад", util.CBGwList)
	_, _ = b.api.SendTextWithKeyboard(ctx, uid, "Выберите розыгрыш:", kb)
}

// showGiveawayDetails — карточка розыгрыша с действиями.
func (b *Bot) showGiveawayDetails(ctx context.Context, uid, id int64) {
	g, err := b.db.GetGiveaway(ctx, id, uid)
	if err != nil {
		_ = b.api.SendText(ctx, uid, "Розыгрыш не найден.")
		return
	}
	text := fmt.Sprintf("🎁 %s\nСтатус: %s\n\n", nonEmpty(g.Name), statusName(g.Status))
	if g.PostText != "" {
		text += fmt.Sprintf("📝 Пост: %s\n", truncate(g.PostText, 100))
	}
	if g.PublishAt != nil {
		text += fmt.Sprintf("📅 Старт: %s\n", util.FormatMoscowTime(*g.PublishAt))
	}
	if g.FinishAt != nil {
		text += fmt.Sprintf("🏁 Итоги: %s\n", util.FormatMoscowTime(*g.FinishAt))
	}
	text += fmt.Sprintf("🏆 Победителей: %d\n", g.WinnersCount)
	if len(g.PublishChannelIDs) > 0 {
		text += fmt.Sprintf("📣 Каналов публикации: %d\n", len(g.PublishChannelIDs))
	}
	if g.CaptchaMode != "" && g.CaptchaMode != store.CaptchaNone {
		text += fmt.Sprintf("🤖 Капча: %s\n", g.CaptchaMode)
	}

	kb := model.NewKeyboard()
	switch g.Status {
	case store.StatusDraft:
		// черновик: продолжить создание / удалить
		kb.AddRow().AddCallBack("▶ Продолжить создание", util.CBGwEdit+fmt.Sprintf("%d", g.ID))
		kb.AddRow().AddCallBack("🗑 Удалить черновик", util.CBGwDelete+fmt.Sprintf("%d", g.ID))
	case store.StatusScheduled:
		kb.AddRow().AddCallBack("✏ Редактировать", util.CBGwEdit+fmt.Sprintf("%d", g.ID))
		kb.AddRow().AddCallBack("🗑 Отменить", util.CBGwCancel+fmt.Sprintf("%d", g.ID))
	case store.StatusActive:
		kb.AddRow().AddCallBack("🗑 Отменить", util.CBGwCancel+fmt.Sprintf("%d", g.ID))
	}
	kb.AddRow().AddCallBack("← Назад", util.CBGwList)
	_, _ = b.api.SendTextWithKeyboard(ctx, uid, text, kb)
}

// showChannels — экран "мои каналы".
// Каналы добавляются вручную кнопкой «➕ Добавить канал» (ID канала или
// пересыл сообщения из него; бот должен быть админом). По ТЗ каналы нужно
// добавлять заранее, чтобы использовать в розыгрышах.
func (b *Bot) showChannels(ctx context.Context, uid int64) {
	items, err := b.db.ListChannels(ctx, uid)
	if err != nil {
		_ = b.api.SendText(ctx, uid, "Ошибка загрузки каналов.")
		return
	}
	var text string
	kb := model.NewKeyboard()
	if len(items) == 0 {
		text = "📢 Мои каналы пусты.\n\n" +
			"Нажмите «➕ Добавить канал» и отправьте ID канала или перешлите\n" +
			"сообщение из него. Бот должен быть администратором в канале."
	} else {
		text = "📢 Мои каналы. Нажмите на канал для деталей/UTM-аналитики.\n" +
			"✅ — бот админ, ⚠️ — бот НЕ админ.\n\n"
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
	_, _ = b.api.SendTextWithKeyboard(ctx, uid, text, kb)
}

// showChannelListForAnalytics — список каналов для просмотра аналитики (alias).
func (b *Bot) showChannelListForAnalytics(ctx context.Context, uid int64) {
	b.showChannels(ctx, uid)
}

// showMyParticipations — экран "мои участия".
func (b *Bot) showMyParticipations(ctx context.Context, uid int64) {
	items, err := b.db.ListMyParticipations(ctx, uid)
	if err != nil || len(items) == 0 {
		_, _ = b.api.SendTextWithKeyboard(ctx, uid, "Вы пока ни в чём не участвуете.", util.BackKeyboard())
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
	_, _ = b.api.SendTextWithKeyboard(ctx, uid, text, util.BackKeyboard())
}

func nonEmpty(s string) string {
	if s == "" {
		return "(без названия)"
	}
	return s
}

// statusName — человекочитаемое название статуса.
func statusName(status string) string {
	switch status {
	case store.StatusDraft:
		return "черновик"
	case store.StatusScheduled:
		return "запланирован"
	case store.StatusActive:
		return "активен"
	case store.StatusFinished:
		return "завершён"
	case store.StatusCancelled:
		return "отменён"
	}
	return status
}

// truncate обрезает текст до max символов, добавляя «…».
func truncate(s string, max int) string {
	if len(s) <= max {
		return s
	}
	return s[:max-1] + "…"
}

// showAnalyticsMenu — экран выбора канала для UTM-аналитики.
// Показывает только каналы, где бот админ (иначе анализировать нечего).
func (b *Bot) showAnalyticsMenu(ctx context.Context, uid int64) {
	channels, err := b.db.ListChannels(ctx, uid)
	if err != nil || len(channels) == 0 {
		_, _ = b.api.SendTextWithKeyboard(ctx, uid,
			"📊 Аналитика\n\nУ вас пока нет каналов.\nДобавьте канал в «Мои каналы» — где бот админ.",
			util.BackKeyboard())
		return
	}

	// Фильтруем только каналы где бот админ.
	var adminChs []store.Channel
	for _, c := range channels {
		if c.IsBotAdmin {
			adminChs = append(adminChs, c)
		}
	}
	if len(adminChs) == 0 {
		_, _ = b.api.SendTextWithKeyboard(ctx, uid,
			"📊 Аналитика\n\nНет каналов, где бот админ.\nСначала добавьте бота админом в канал.",
			util.BackKeyboard())
		return
	}

	kb := model.NewKeyboard()
	for _, c := range adminChs {
		title := c.Title
		if title == "" {
			title = fmt.Sprintf("канал #%d", c.ChatID)
		}
		kb.AddRow().AddCallBack("📊 "+title, util.CBChannelUTM+fmt.Sprintf("%d", c.ID))
	}
	kb.AddRow().AddCallBack("🏠 В меню", util.CBMainMenu)
	_, _ = b.api.SendTextWithKeyboard(ctx, uid,
		"📊 Аналитика источников подписчиков\n\nВыберите канал для просмотра статистики:",
		kb)
}
