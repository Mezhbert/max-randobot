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
	// Защита от устаревшего шага (старые черновики: referral/promotion):
	// промпта нет → пустое сообщение MAX молча отвергнет. Сбрасываем на первый.
	if fsm.PromptForStep(g.DraftStep) == "" {
		_ = b.db.SetDraftStep(ctx, g.ID, fsm.FirstStep)
		g.DraftStep = fsm.FirstStep
		b.showStep(ctx, userID, g, fsm.FirstStep)
		return
	}
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

// advance переходит к следующему АКТУАЛЬНОМУ шагу и показывает его приглашение.
// Условные шаги (publish_at/finish_at/finish_count/results_text) перескакивает
// сам движок (fsm.GoNext + fsm.StepSkipped) — по текущему режиму розыгрыша.
func (b *Bot) advance(ctx context.Context, userID int64, giveawayID int64, currentStep string) {
	g, err := b.db.GetGiveawayAny(ctx, giveawayID)
	if err != nil {
		_ = b.api.SendText(ctx, userID, "⚠️ Не удалось получить черновик: "+err.Error())
		return
	}
	next, err := b.engine.GoNext(ctx, giveawayID, currentStep, g)
	if err != nil {
		_ = b.api.SendText(ctx, userID, "⚠️ Ошибка перехода: "+err.Error())
		return
	}
	if next == currentStep {
		// Конец цепочки не наступает здесь (preview терминальный), защита.
		return
	}
	// При уходе со шага выбора каналов — сбрасываем mid (сообщение больше не редактируем).
	if (currentStep == fsm.StepSubChannels || currentStep == fsm.StepPubChannels) && next != currentStep {
		_ = b.db.UpdateGiveawayField(ctx, giveawayID, "channel_select_mid", nil)
		g.ChannelSelectMID = ""
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
		attachments := util.GiveawayAttachments(buttonText, g.ID, mediaSimple, true, b.botUsername, b.botUserID)
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
// Всегда отправляет НОВОЕ сообщение и запоминает mid: возврат к шагу (Назад,
// Продолжить после подсказки) должен быть ВИДИМЫМ — редактирование старого
// сообщения выше выглядело как «ничего не произошло». Тогглы же (нажатие на
// канал) редактируют сообщение на месте — см. toggleChannelSelection.
func (b *Bot) showChannelSelectStep(ctx context.Context, userID int64, g store.Giveaway, step string) {
	// Ранний чек для каналов публикации: без хотя бы одного канала с админкой
	// бота шаг бессмыслен (валидация «в конце» — плохо, см. заметки итерации).
	if step == fsm.StepPubChannels {
		if !b.hasAdminChannels(ctx, userID) {
			_ = b.api.SendText(ctx, userID,
				"⚠️ Нет каналов, где бот — админ. Сначала добавьте канал в «Мои каналы» 👇\n\n"+
					"Как: добавьте бота админом в канал и перешлите оттуда любое сообщение.")
			b.showChannels(ctx, userID)
			return
		}
	}

	text, kb := buildChannelSelectKB(ctx, b, userID, g, step)
	mid, _ := b.api.SendTextWithKeyboard(ctx, userID, text, kb)
	if mid != "" {
		_ = b.db.UpdateGiveawayField(ctx, g.ID, "channel_select_mid", mid)
	}
}

// hasAdminChannels — есть ли у юзера хоть один канал, где бот админ.
func (b *Bot) hasAdminChannels(ctx context.Context, uid int64) bool {
	channels, err := b.db.ListChannels(ctx, uid)
	if err != nil {
		return false
	}
	for _, c := range channels {
		if c.IsBotAdmin {
			return true
		}
	}
	return false
}

// buildChannelSelectKB строит текст + клавиатуру для шага выбора каналов.
func buildChannelSelectKB(ctx context.Context, b *Bot, userID int64, g store.Giveaway, step string) (string, *model.Keyboard) {
	channels, _ := b.db.ListChannels(ctx, userID)
	prefix := util.CBChannelToggleSub
	selected := g.SubscribeChannelIDs
	title := "📢 Каналы для подписки"
	if step == fsm.StepPubChannels {
		prefix = util.CBChannelTogglePub
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
	case fsm.StepName:
		// Первый шаг — Назад идти некуда: только 💡 и Отменить.
		return util.FirstStepKeyboard()
	}
	return util.NavKeyboard()
}
