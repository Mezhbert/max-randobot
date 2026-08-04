package fsm

import (
	"context"
	"errors"
	"strconv"
	"strings"

	"github.com/maxbot/giveawaybot/internal/store"
	"github.com/maxbot/giveawaybot/internal/util"
)

// HandleTextInput обрабатывает текстовый ввод пользователя для текущего шага.
// Возвращает текст ошибки (пустая строка = успех), если формат не соответствует.
func (e *Engine) HandleTextInput(ctx context.Context, g store.Giveaway, text string) error {
	switch g.DraftStep {
	case StepPostText:
		return e.DB.UpdateGiveawayField(ctx, g.ID, "post_text", text)

	case StepName:
		if strings.TrimSpace(text) == "" {
			return errors.New("название не может быть пустым")
		}
		return e.DB.UpdateGiveawayField(ctx, g.ID, "name", text)

	case StepButtonText:
		if strings.TrimSpace(text) == "" {
			text = "участвовать"
		}
		return e.DB.UpdateGiveawayField(ctx, g.ID, "button_text", text)

	case StepPublishAt:
		t, err := util.ParseMoscowTime(text)
		if err != nil {
			return err
		}
		return e.DB.UpdateGiveawayField(ctx, g.ID, "publish_at", t)

	case StepFinishAt:
		t, err := util.ParseMoscowTime(text)
		if err != nil {
			return err
		}
		return e.DB.UpdateGiveawayField(ctx, g.ID, "finish_at", t)

	case StepWinners:
		n, err := strconv.Atoi(strings.TrimSpace(text))
		if err != nil || n < 1 || n > 100 {
			return errors.New("число победителей должно быть целым от 1 до 100")
		}
		return e.DB.UpdateGiveawayField(ctx, g.ID, "winners_count", n)

	case StepWinnersMsg:
		return e.DB.UpdateGiveawayField(ctx, g.ID, "winners_message", text)

	// Шаги с выбором каналов / опциями обрабатываются через callback, не текстом.
	case StepMedia, StepSubChannels, StepPubChannels, StepResultsLoc,
		StepReferral, StepCaptcha, StepPromotion, StepPreview:
		return errors.New("используйте кнопки под сообщением")
	}
	return errors.New("неизвестный шаг")
}

// SkipStep помечает опциональный шаг как пропущенный и переходит дальше.
func (e *Engine) SkipStep(ctx context.Context, g store.Giveaway) error {
	if !IsOptional(g.DraftStep) {
		return errors.New("этот шаг нельзя пропустить")
	}
	switch g.DraftStep {
	case StepWinnersMsg:
		// ничего не пишем — остаётся пустое сообщение
	case StepReferral:
		_ = e.DB.UpdateGiveawayField(ctx, g.ID, "referral_enabled", false)
	case StepCaptcha:
		_ = e.DB.UpdateGiveawayField(ctx, g.ID, "captcha_mode", store.CaptchaNone)
	case StepPromotion:
		_ = e.DB.UpdateGiveawayField(ctx, g.ID, "promotion_enabled", false)
	}
	return nil
}
