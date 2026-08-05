package handlers

import (
	"context"
	"fmt"

	"github.com/maxbot/giveawaybot/internal/fsm"
	"github.com/maxbot/giveawaybot/internal/store"
	"github.com/maxbot/giveawaybot/internal/util"
	"github.com/max-messenger/max-bot-api-client-go/v2/model"
)

// handleDraftInput — текст пользователя в режиме создания розыгрыша.
func (b *Bot) handleDraftInput(ctx context.Context, userID int64, g store.Giveaway, text string) {
	if err := b.engine.HandleTextInput(ctx, g, text); err != nil {
		// Неверный формат — повторяем запрос того же шага.
		_, _ = b.api.SendTextWithKeyboard(ctx, userID,
			"⚠️ "+err.Error()+"\n\n"+fsm.PromptForStep(g.DraftStep),
			kbForStep(g.DraftStep))
		return
	}
	// Переход к следующему шагу
	b.advance(ctx, userID, g.ID, g.DraftStep)
}

// advance переходит к следующему шагу и показывает его приглашение.
func (b *Bot) advance(ctx context.Context, userID int64, giveawayID int64, currentStep string) {
	g, err := b.db.GetGiveawayAny(ctx, giveawayID)
	if err != nil {
		_ = b.api.SendText(ctx, userID, "⚠️ Не удалось получить черновик: "+err.Error())
		return
	}
	next, err := b.engine.GoNext(ctx, giveawayID, currentStep)
	if err != nil {
		_ = b.api.SendText(ctx, userID, "⚠️ Ошибка перехода: "+err.Error())
		return
	}
	// При уходе со шага выбора каналов — сбрасываем mid (сообщение больше не редактируем).
	if (currentStep == fsm.StepSubChannels || currentStep == fsm.StepPubChannels) && next != currentStep {
		_ = b.db.UpdateGiveawayField(ctx, giveawayID, "channel_select_mid", nil)
		g.ChannelSelectMID = ""
	}
	b.showStep(ctx, userID, g, next)
}

// advanceSkipping переходит к следующему шагу, но если он совпадает с skip —
// делает ещё один переход. Используется, чтобы перепрыгнуть условный шаг
// (например, results_text при выборе «итоги в исходном посте»).
func (b *Bot) advanceSkipping(ctx context.Context, userID int64, giveawayID int64, currentStep, skip string) {
	g, err := b.db.GetGiveawayAny(ctx, giveawayID)
	if err != nil {
		_ = b.api.SendText(ctx, userID, "⚠️ Не удалось получить черновик: "+err.Error())
		return
	}
	next, err := b.engine.GoNext(ctx, giveawayID, currentStep)
	if err != nil {
		_ = b.api.SendText(ctx, userID, "⚠️ Ошибка перехода: "+err.Error())
		return
	}
	if next == skip {
		if next2, err := b.engine.GoNext(ctx, giveawayID, next); err == nil {
			next = next2
		}
	}
	b.showStep(ctx, userID, g, next)
}

// showStep показывает приглашение конкретного шага.
func (b *Bot) showStep(ctx context.Context, userID int64, g store.Giveaway, step string) {
	if step == fsm.StepPreview {
		// Предпросмотр: ПОЛНЫЙ пост ровно как будет опубликован
		// (текст + медиа + кнопка участия в виде "превью"),
		// затем — сводка-инфо со стрелкой ↑ на пост выше + кнопки Принять/Отклонить.
		buttonText := g.ButtonText
		if buttonText == "" {
			buttonText = "Участвовать"
		}
		// Медиа-токены → упрощённая форма для хелпера.
		mediaSimple := make([]util.MediaTokenSimple, 0, len(g.MediaTokens))
		for _, mt := range g.MediaTokens {
			mediaSimple = append(mediaSimple, util.MediaTokenSimple{Type: mt.Type, Token: mt.Token})
		}
		// 1) Полный предпросмотр поста (как будет в канале).
		attachments := util.GiveawayAttachments(buttonText, g.ID, mediaSimple, true, b.botUsername)
		_ = b.api.SendWithAttachments(ctx, userID, g.PostText, attachments, model.TextFormat(g.PostFormat))

		// 2) Если итоги отдельным постом — тоже предпросмотрим.
		if !g.ResultsInOriginalPost && g.ResultsPostText != "" {
			_ = b.api.SendText(ctx, userID, g.ResultsPostText)
		}

		// 3) Сводка со стрелкой ↑ (указывает на пост выше) + кнопки подтверждения.
		summary := "⬆️ Так будет выглядеть пост выше (с кнопкой «" + buttonText + "»).\n\n" +
			fsm.FormatPreview(g, b.db)
		_, _ = b.api.SendTextWithKeyboard(ctx, userID, summary, util.ConfirmKeyboard())
		return
	}

	// Шаги выбора каналов — рендерим список «моих каналов» с галочками.
	if step == fsm.StepSubChannels || step == fsm.StepPubChannels {
		b.showChannelSelectStep(ctx, userID, g, step)
		return
	}

	_, _ = b.api.SendTextWithKeyboard(ctx, userID, fsm.PromptForStep(step), kbForStep(step))
}

// showChannelSelectStep — шаг выбора каналов с живыми галочками (тогглинг).
// При первом показе — отправляет новое сообщение и запоминает mid; при повторных
// (тоггл) — редактирует то же сообщение через EditChatText (edit-in-place).
func (b *Bot) showChannelSelectStep(ctx context.Context, userID int64, g store.Giveaway, step string) {
	text, kb := buildChannelSelectKB(ctx, b, userID, g, step)

	// Если уже есть запомненный mid — редактируем (edit-in-place).
	if g.ChannelSelectMID != "" {
		if err := b.api.EditChatText(ctx, g.ChannelSelectMID, text, []model.Attachment{kb.Build()}, ""); err != nil {
			// Edit упал (напр. истёк лимит времени) — шлём новым сообщением.
			mid, _ := b.api.SendTextWithKeyboard(ctx, userID, text, kb)
			_ = b.db.UpdateGiveawayField(ctx, g.ID, "channel_select_mid", mid)
		}
		return
	}

	// Первая отрисовка — отправляем и запоминаем mid.
	mid, _ := b.api.SendTextWithKeyboard(ctx, userID, text, kb)
	if mid != "" {
		_ = b.db.UpdateGiveawayField(ctx, g.ID, "channel_select_mid", mid)
	}
}

// buildChannelSelectKB строит текст + клавиатуру для шага выбора каналов.
func buildChannelSelectKB(ctx context.Context, b *Bot, userID int64, g store.Giveaway, step string) (string, *model.Keyboard) {
	channels, _ := b.db.ListChannels(ctx, userID)
	prefix := "ch_toggle_sub:"
	selected := g.SubscribeChannelIDs
	title := "📢 Каналы для подписки"
	if step == fsm.StepPubChannels {
		prefix = "ch_toggle_pub:"
		selected = g.PublishChannelIDs
		title = "📣 Каналы публикации"
	}

	kb := model.NewKeyboard()
	if len(channels) > 0 {
		for _, c := range channels {
			mark := "⬜ "
			for _, sid := range selected {
				if sid == c.ID {
					mark = "✅ "
					break
				}
			}
			ctitle := c.Title
			if ctitle == "" {
				ctitle = fmt.Sprintf("канал #%d", c.ChatID)
			}
			if !c.IsBotAdmin {
				ctitle += " ⚠️"
			}
			kb.AddRow().AddCallBack(mark+ctitle, prefix+fmt.Sprintf("%d", c.ID))
		}
	}
	kb.AddRow().AddCallBack("Далее →", util.CBFSMOption+"next")
	kb.AddRow().
		AddCallBack("← Назад", util.CBFSMBack).
		AddCallBack("✖ Отменить", util.CBFSMCancel)

	text := title + ".\n\n" +
		"Нажимайте на каналы, чтобы отметить/снять. ⚠️ — бот НЕ админ (нельзя использовать).\n" +
		"Готово — «Далее →»."
	return text, kb
}

// kbForStep возвращает клавиатуру для шага (навигация + опции).
// «Сохранить в черновик» убран — черновик сохраняется автоматически после ввода названия.
func kbForStep(step string) *model.Keyboard {
	switch step {
	case fsm.StepMedia, fsm.StepWinnersMsg, fsm.StepButtonText, fsm.StepResultsText:
		return util.NavWithSkipKeyboard()
	case fsm.StepSubChannels, fsm.StepPubChannels:
		kb := model.NewKeyboard()
		kb.AddRow().AddCallBack("Далее →", util.CBFSMOption+"next")
		kb.AddRow().
			AddCallBack("← Назад", util.CBFSMBack).
			AddCallBack("✖ Отменить", util.CBFSMCancel)
		return kb
	case fsm.StepResultsLoc:
		kb := model.NewKeyboard()
		kb.AddRow().
			AddCallBack("В исходном посте", util.CBFSMOption+"results_original").
			AddCallBack("Отдельным постом", util.CBFSMOption+"results_separate")
		kb.AddRow().
			AddCallBack("← Назад", util.CBFSMBack).
			AddCallBack("✖ Отменить", util.CBFSMCancel)
		return kb
	case fsm.StepCaptcha:
		kb := model.NewKeyboard()
		kb.AddRow().
			AddCallBack("Нет", util.CBFSMOption+"captcha_none").
			AddCallBack("Для всех", util.CBFSMOption+"captcha_all").
			AddCallBack("Для процента", util.CBFSMOption+"captcha_percent")
		kb.AddRow().
			AddCallBack("⏭ Пропустить", util.CBFSMSkip).
			AddCallBack("← Назад", util.CBFSMBack).
			AddCallBack("✖ Отменить", util.CBFSMCancel)
		return kb
	case fsm.StepPreview:
		return util.ConfirmKeyboard()
	}
	return util.NavKeyboard()
}
