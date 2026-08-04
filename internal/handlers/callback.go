package handlers

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"

	"github.com/maxbot/giveawaybot/internal/fsm"
	"github.com/maxbot/giveawaybot/internal/store"
	"github.com/maxbot/giveawaybot/internal/util"
	"github.com/max-messenger/max-bot-api-client-go/v2/model"
)

// onMessageCallback — обработка нажатий на inline-кнопки.
func (b *Bot) onMessageCallback(ctx context.Context, u model.Update) {
	cb := u.GetCallback()
	defer b.api.AnswerCallback(ctx, cb.CallbackID, "") // снять "часики" с кнопки

	uid := cb.User.UserID
	p := cb.Payload

	// 1. Кнопка "Участвовать" под постом розыгрыша
	if strings.HasPrefix(p, util.CBParticipate) {
		b.onParticipate(ctx, uid, strings.TrimPrefix(p, util.CBParticipate))
		return
	}

	// 2. Кнопки FSM создания розыгрыша
	if isFSMPayload(p) {
		b.handleFSMCallback(ctx, uid, p)
		return
	}

	// 3. Главное меню и навигация
	switch p {
	case util.CBMainMenu:
		b.sendMainMenu(ctx, uid, "Главное меню:")
	case util.CBCreateGiveaway:
		b.startCreate(ctx, uid)
	case util.CBMyGiveaways:
		b.showGiveawayFilters(ctx, uid)
	case util.CBMyChannels:
		b.showChannels(ctx, uid)
	case util.CBMyParticipations:
		b.showMyParticipations(ctx, uid)
	case util.CBSupport:
		_ = b.api.SendTextWithKeyboard(ctx, uid,
			"🆘 Поддержка\n\nПо всем вопросам пишите: @your_support_contact\n"+
				"Или email: support@example.com", util.BackKeyboard())
	case util.CBChannelAdd:
		b.startAddChannel(ctx, uid)
	case util.CBChannelBack:
		b.showChannels(ctx, uid)
	case util.CBChannelAnalytics:
		b.showChannelListForAnalytics(ctx, uid)
	case util.CBChannelRefresh:
		b.refreshChannels(ctx, uid)
	default:
		// подпункты (фильтры/открыть/удалить и т.д.)
		b.handleSubPayload(ctx, uid, p)
	}
}

// isFSMPayload определяет кнопки FSM (назад/отмена/пропустить/опции/подтверждение).
func isFSMPayload(p string) bool {
	switch p {
	case util.CBFSMBack, util.CBFSMCancel, util.CBFSMSkip, util.CBFSMConfirm, util.CBFSMReject:
		return true
	}
	return strings.HasPrefix(p, util.CBFSMOption)
}

// startCreate начинает создание розыгрыша.
func (b *Bot) startCreate(ctx context.Context, uid int64) {
	g, err := b.engine.StartDraft(ctx, uid)
	if err != nil {
		_ = b.api.SendText(ctx, uid, "⚠️ Не удалось начать создание: "+err.Error())
		return
	}
	b.showStep(ctx, uid, g, g.DraftStep)
}

// handleFSMCallback обрабатывает кнопки FSM (назад/отмена/пропустить/опции/принять/отклонить).
func (b *Bot) handleFSMCallback(ctx context.Context, uid int64, p string) {
	g, err := b.db.GetDraftGiveaway(ctx, uid)
	if err != nil {
		_ = b.api.SendTextWithKeyboard(ctx, uid,
			"Создание не активно. Начните заново.", util.MainMenuKeyboard())
		return
	}

	switch {
	case p == util.CBFSMCancel:
		_ = b.engine.Abandon(ctx, g.ID)
		b.sendMainMenu(ctx, uid, "✖ Создание отменено. Черновик сохранён в «Мои розыгрыши → В процессе создания».")

	case p == util.CBFSMBack:
		prev, _ := b.engine.GoBack(ctx, g.ID, g.DraftStep)
		// перечитываем, чтобы актуальные данные
		gg, _ := b.db.GetGiveawayAny(ctx, g.ID)
		b.showStep(ctx, uid, gg, prev)

	case p == util.CBFSMSkip:
		if err := b.engine.SkipStep(ctx, g); err != nil {
			b.api.AnswerCallback(ctx, "", err.Error())
			return
		}
		b.advance(ctx, uid, g.ID, g.DraftStep)

	case p == util.CBFSMConfirm:
		if err := b.engine.Confirm(ctx, g.ID); err != nil {
			var ve *fsm.ValidationError
			if errors.As(err, &ve) {
				_ = b.api.SendText(ctx, uid, "⚠️ "+ve.Msg)
			} else {
				_ = b.api.SendText(ctx, uid, "⚠️ Ошибка: "+err.Error())
			}
			return
		}
		b.sendMainMenu(ctx, uid, "✅ Розыгрыш создан и запланирован! Он будет опубликован по расписанию.")

	case p == util.CBFSMReject:
		_ = b.engine.Abandon(ctx, g.ID)
		b.sendMainMenu(ctx, uid, "❌ Создание отклонено. Черновик сохранён в «Мои розыгрыши → В процессе создания».")

	case strings.HasPrefix(p, util.CBFSMOption):
		key := strings.TrimPrefix(p, util.CBFSMOption)
		b.handleOption(ctx, uid, g, key)
	}
}

// handleOption обрабатывает опционные кнопки шагов (выбор каналов, режимы и т.д.).
func (b *Bot) handleOption(ctx context.Context, uid int64, g store.Giveaway, key string) {
	switch key {
	case "next":
		// шаги sub/pub channels переходят дальше по «Далее»
		b.advance(ctx, uid, g.ID, g.DraftStep)

	case "results_original":
		_ = b.db.UpdateGiveawayField(ctx, g.ID, "results_in_original_post", true)
		b.advance(ctx, uid, g.ID, g.DraftStep)
	case "results_separate":
		_ = b.db.UpdateGiveawayField(ctx, g.ID, "results_in_original_post", false)
		// Запросить текст поста с итогами
		_ = b.db.SetDraftStep(ctx, g.ID, fsm.StepResultsLoc+"_msg")
		_ = b.api.SendTextWithKeyboard(ctx, uid,
			"Отправьте текст отдельного поста с итогами розыгрыша.",
			util.NavKeyboard())

	case "referral_on":
		_ = b.db.UpdateGiveawayField(ctx, g.ID, "referral_enabled", true)
		b.advance(ctx, uid, g.ID, g.DraftStep)
	case "referral_off":
		_ = b.db.UpdateGiveawayField(ctx, g.ID, "referral_enabled", false)
		b.advance(ctx, uid, g.ID, g.DraftStep)

	case "captcha_none":
		_ = b.db.UpdateGiveawayField(ctx, g.ID, "captcha_mode", store.CaptchaNone)
		b.advance(ctx, uid, g.ID, g.DraftStep)
	case "captcha_all":
		_ = b.db.UpdateGiveawayField(ctx, g.ID, "captcha_mode", store.CaptchaAll)
		b.advance(ctx, uid, g.ID, g.DraftStep)
	case "captcha_percent":
		_ = b.db.UpdateGiveawayField(ctx, g.ID, "captcha_mode", store.CaptchaPercent)
		_ = b.api.SendText(ctx, uid, "Введите процент участников (0-100), которым показывать капчу:")
		// Дальнейший ввод обрабатывается через session (см. SessionCaptchaPct).
		_ = b.db.SetSession(ctx, uid, store.SessionCaptchaPct)

	case "promotion_on":
		_ = b.db.UpdateGiveawayField(ctx, g.ID, "promotion_enabled", true)
		b.advance(ctx, uid, g.ID, g.DraftStep)
	case "promotion_off":
		_ = b.db.UpdateGiveawayField(ctx, g.ID, "promotion_enabled", false)
		b.advance(ctx, uid, g.ID, g.DraftStep)
	}
}

// handleSubPayload — подпункты меню (фильтры, открыть, удалить и т.д.).
func (b *Bot) handleSubPayload(ctx context.Context, uid int64, p string) {
	switch {
	case strings.HasPrefix(p, util.CBGiveawayFilter):
		status := strings.TrimPrefix(p, util.CBGiveawayFilter)
		b.showGiveawaysByStatus(ctx, uid, status)

	case strings.HasPrefix(p, util.CBGwOpen):
		id, _ := strconv.ParseInt(strings.TrimPrefix(p, util.CBGwOpen), 10, 64)
		b.showGiveawayDetails(ctx, uid, id)

	case strings.HasPrefix(p, util.CBGwCancel):
		id, _ := strconv.ParseInt(strings.TrimPrefix(p, util.CBGwCancel), 10, 64)
		if err := b.db.SetStatus(ctx, id, store.StatusCancelled); err == nil {
			_ = b.api.SendText(ctx, uid, "Запланированный розыгрыш отменён.")
		}

	case strings.HasPrefix(p, util.CBGwDelete):
		id, _ := strconv.ParseInt(strings.TrimPrefix(p, util.CBGwDelete), 10, 64)
		if err := b.db.DeleteGiveaway(ctx, id, uid); err == nil {
			_ = b.api.SendText(ctx, uid, "Черновик удалён.")
		} else {
			_ = b.api.SendText(ctx, uid, "Нельзя удалить завершённый розыгрыш.")
		}

	case p == util.CBGwList:
		b.showGiveawayFilters(ctx, uid)

	case strings.HasPrefix(p, "ch_toggle_sub:"), strings.HasPrefix(p, "ch_toggle_pub:"):
		b.toggleChannelSelection(ctx, uid, p)

	case strings.HasPrefix(p, util.CBChannelUTM):
		// Выбор канала для генерации UTM-ссылки
		b.showUTMMenu(ctx, uid, p)

	case strings.HasPrefix(p, util.CBUTMCreate):
		// Начать создание UTM-ссылки на канал
		b.startCreateUTM(ctx, uid, p)

	case strings.HasPrefix(p, util.CBUTMStats):
		// Показать аналитику UTM по каналу
		b.showUTMStats(ctx, uid, p)

	case strings.HasPrefix(p, util.CBUTMExport):
		// Экспорт CSV аналитики
		b.exportUTMCSV(ctx, uid, p)

	default:
		b.sendMainMenu(ctx, uid, "Главное меню:")
	}
}

// fmtErr — короткий текст ошибки для пользователя.
func fmtErr(err error) string {
	if err == nil {
		return ""
	}
	return fmt.Sprintf(" (%v)", err)
}
