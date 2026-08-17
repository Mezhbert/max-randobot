package handlers

import (
	"context"
	"fmt"
	"time"

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
	b.advance(ctx, userID, "", g.ID, g.DraftStep)
}

// advance переходит к следующему шагу и показывает его приглашение.
// cbID ≠ "" → шаг рисуется edit-in-place в нажатом сообщении (без скролла).
// Условные шаги (publish_at/finish_at/finish_count) авто-пропускаются
// по выбранному режиму: publish_now → без publish_at; finish=time → без
// finish_count; finish=count → без finish_at.
func (b *Bot) advance(ctx context.Context, userID int64, cbID string, giveawayID int64, currentStep string) {
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
	// Авто-пропуск условных шагов по режиму (цепочкой — вдруг два подряд).
	for skipConditional(g, next) {
		n2, err := b.engine.GoNext(ctx, giveawayID, next)
		if err != nil {
			break
		}
		next = n2
	}
	if cbID != "" {
		b.showStepCB(ctx, userID, cbID, g, next)
	} else {
		b.showStep(ctx, userID, g, next)
	}
}

// skipConditional — нужно ли пропустить шаг в зависимости от режима розыгрыша.
func skipConditional(g store.Giveaway, step string) bool {
	switch step {
	case fsm.StepFinishCount:
		return g.FinishMode != "count" // режим по времени → count не нужен
	case fsm.StepFinishAt:
		return g.FinishMode == "count" // режим по количеству → дата не нужна
	}
	return false
}

// advanceSkipping переходит к следующему шагу, но если он совпадает с skip —
// делает ещё один переход. Используется, чтобы перепрыгнуть условный шаг
// (например, results_text при выборе «итоги в исходном посте»).
func (b *Bot) advanceSkipping(ctx context.Context, userID int64, cbID string, giveawayID int64, currentStep, skip string) {
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
	if cbID != "" {
		b.showStepCB(ctx, userID, cbID, g, next)
	} else {
		b.showStep(ctx, userID, g, next)
	}
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

// showStepCB — showStep для клика по кнопке: рисует шаг EDIT-IN-PLACE
// (в нажатом сообщении) — юзер не скроллит, ответ под пальцем.
// Preview и выбор каналов шлются отдельно (медиа/тогглинг).
func (b *Bot) showStepCB(ctx context.Context, userID int64, cbID string, g store.Giveaway, step string) {
	if step == fsm.StepPreview || step == fsm.StepSubChannels || step == fsm.StepPubChannels {
		b.showStep(ctx, userID, g, step)
		return
	}
	b.render(ctx, userID, cbID, fsm.PromptForStep(step), kbForStep(step))
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
		AddCallBack("💡", util.CBFSMHint).
		AddCallBack("← Назад", util.CBFSMBack).
		AddCallBack("✖ Отменить", util.CBFSMCancel)

	text := title + ".\n\n" +
		"Нажимайте на каналы, чтобы отметить/снять. ⚠️ — бот НЕ админ (нельзя использовать).\n" +
		"Готово — «Далее →»."
	return text, kb
}

// kbForStep возвращает клавиатуру для шага (навигация + опции + 💡 подсказка).
// Для шагов дат добавлена кнопка 📋 — копирует шаблон текущего времени МСК
// в буфер обмена (type=clipboard), чтобы вставить и поправить.
func kbForStep(step string) *model.Keyboard {
	switch step {
	case fsm.StepPublishAt, fsm.StepFinishAt:
		kb := model.NewKeyboard()
		kb.AddRow().AddClipboard("📋 Скопировать формат даты", util.FormatMoscowTime(time.Now().Add(time.Hour)))
		kb.AddRow().
			AddCallBack("💡", util.CBFSMHint).
			AddCallBack("← Назад", util.CBFSMBack).
			AddCallBack("✖ Отменить", util.CBFSMCancel)
		return kb
	case fsm.StepPublishMode:
		kb := model.NewKeyboard()
		kb.AddRow().
			AddCallBack("🚀 Опубликовать сразу", util.CBFSMOption+"publish_now").
			AddCallBack("🕐 По времени", util.CBFSMOption+"publish_later")
		kb.AddRow().
			AddCallBack("💡", util.CBFSMHint).
			AddCallBack("← Назад", util.CBFSMBack).
			AddCallBack("✖ Отменить", util.CBFSMCancel)
		return kb
	case fsm.StepFinishMode:
		kb := model.NewKeyboard()
		kb.AddRow().
			AddCallBack("⏰ По времени", util.CBFSMOption+"finish_time").
			AddCallBack("👥 По количеству участников", util.CBFSMOption+"finish_count")
		kb.AddRow().
			AddCallBack("💡", util.CBFSMHint).
			AddCallBack("← Назад", util.CBFSMBack).
			AddCallBack("✖ Отменить", util.CBFSMCancel)
		return kb
	case fsm.StepFinishCount:
		kb := model.NewKeyboard()
		kb.AddRow().
			AddCallBack("💡", util.CBFSMHint).
			AddCallBack("← Назад", util.CBFSMBack).
			AddCallBack("✖ Отменить", util.CBFSMCancel)
		return kb
	case fsm.StepMedia, fsm.StepWinnersMsg, fsm.StepButtonText, fsm.StepResultsText:
		return util.NavWithSkipKeyboard()
	case fsm.StepSubChannels, fsm.StepPubChannels:
		kb := model.NewKeyboard()
		kb.AddRow().AddCallBack("Далее →", util.CBFSMOption+"next")
		kb.AddRow().
			AddCallBack("💡", util.CBFSMHint).
			AddCallBack("← Назад", util.CBFSMBack).
			AddCallBack("✖ Отменить", util.CBFSMCancel)
		return kb
	case fsm.StepResultsLoc:
		kb := model.NewKeyboard()
		kb.AddRow().
			AddCallBack("В исходном посте", util.CBFSMOption+"results_original").
			AddCallBack("Отдельным постом", util.CBFSMOption+"results_separate")
		kb.AddRow().
			AddCallBack("💡", util.CBFSMHint).
			AddCallBack("← Назад", util.CBFSMBack).
			AddCallBack("✖ Отменить", util.CBFSMCancel)
		return kb
	case fsm.StepCaptcha:
		kb := model.NewKeyboard()
		kb.AddRow().
			AddCallBack("✅ Да", util.CBFSMOption+"captcha_all").
			AddCallBack("❌ Нет", util.CBFSMOption+"captcha_none")
		kb.AddRow().
			AddCallBack("💡", util.CBFSMHint).
			AddCallBack("← Назад", util.CBFSMBack).
			AddCallBack("✖ Отменить", util.CBFSMCancel)
		return kb
	case fsm.StepPreview:
		return util.ConfirmKeyboard()
	}
	return util.NavKeyboard()
}
