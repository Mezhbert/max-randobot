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
		_ = b.api.SendTextWithKeyboard(ctx, userID,
			"⚠️ "+err.Error()+"\n\n"+fsm.PromptForStep(g.DraftStep),
			kbForStep(g.DraftStep))
		return
	}
	// Переход к следующему шагу
	b.advance(ctx, userID, g.ID, g.DraftStep)
}

// advance переходит к следующему шагу и показывает его приглашение.
func (b *Bot) advance(ctx context.Context, userID, giveawayID int64, currentStep string) {
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
	b.showStep(ctx, userID, g, next)
}

// showStep показывает приглашение конкретного шага.
func (b *Bot) showStep(ctx context.Context, userID int64, g store.Giveaway, step string) {
	if step == fsm.StepPreview {
		// Предпросмотр: сначала пост, потом (если отдельным постом) пост итогов,
		// затем сводка + кнопки Принять/Отклонить.
		text := "👁 ПРЕДПРОСМОТР ПОСТА РОЗЫГРЫША:\n\n" + g.PostText
		_ = b.api.SendTextWithKeyboard(ctx, userID, text, util.CancelOnlyKeyboard())
		if !g.ResultsInOriginalPost && g.ResultsPostText != "" {
			_ = b.api.SendText(ctx, userID, "👁 ПРЕДПРОСМОТР ПОСТА С ИТОГАМИ:\n\n"+g.ResultsPostText)
		}
		_ = b.api.SendTextWithKeyboard(ctx, userID, fsm.FormatPreview(g, b.db), util.ConfirmKeyboard())
		return
	}

	// Шаги выбора каналов — рендерим список «моих каналов» с галочками.
	if step == fsm.StepSubChannels || step == fsm.StepPubChannels {
		b.showChannelSelectStep(ctx, userID, g, step)
		return
	}

	_ = b.api.SendTextWithKeyboard(ctx, userID, fsm.PromptForStep(step), kbForStep(step))
}

// showChannelSelectStep — шаг выбора каналов с живыми галочками (тогглинг).
func (b *Bot) showChannelSelectStep(ctx context.Context, userID int64, g store.Giveaway, step string) {
	channels, err := b.db.ListChannels(ctx, userID)
	prefix := "ch_toggle_sub:"
	selected := g.SubscribeChannelIDs
	title := "📢 Каналы для подписки"
	if step == fsm.StepPubChannels {
		prefix = "ch_toggle_pub:"
		selected = g.PublishChannelIDs
		title = "📣 Каналы публикации"
	}

	kb := model.NewKeyboard()
	if err == nil && len(channels) > 0 {
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
	} else {
		kb.AddRow().AddCallBack("➕ Добавить канал (сначала)", util.CBChannelAdd)
	}
	kb.AddRow().AddCallBack("Далее →", util.CBFSMOption+"next")
	kb.AddRow().
		AddCallBack("← Назад", util.CBFSMBack).
		AddCallBack("✖ Отменить", util.CBFSMCancel)

	text := title + ".\n\n" +
		"Нажимайте на каналы, чтобы отметить/снять. ⚠️ — бот НЕ админ (нельзя использовать).\n" +
		"Готово — «Далее →»."
	_ = b.api.SendTextWithKeyboard(ctx, userID, text, kb)
}

// kbForStep возвращает клавиатуру для шага (навигация + опции).
func kbForStep(step string) *model.Keyboard {
	switch step {
	case fsm.StepMedia, fsm.StepWinnersMsg:
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
	case fsm.StepReferral:
		kb := model.NewKeyboard()
		kb.AddRow().
			AddCallBack("✅ Включить", util.CBFSMOption+"referral_on").
			AddCallBack("❌ Нет", util.CBFSMOption+"referral_off")
		kb.AddRow().
			AddCallBack("⏭ Пропустить", util.CBFSMSkip).
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
	case fsm.StepPromotion:
		kb := model.NewKeyboard()
		kb.AddRow().
			AddCallBack("✅ Включить", util.CBFSMOption+"promotion_on").
			AddCallBack("❌ Нет", util.CBFSMOption+"promotion_off")
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
