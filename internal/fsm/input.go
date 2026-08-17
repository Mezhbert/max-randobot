package fsm

import (
	"context"
	"errors"
	"fmt"
	"strconv"
	"strings"
	"time"

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
		// Валидация на шаге (не в конце!): publish_at должен быть в будущем.
		// Это убирает сюрприз «всё заполнил, а потом ошибка».
		if !t.After(time.Now()) {
			return fmt.Errorf("время публикации %s уже прошло — введите будущее время",
				t.In(util.MoscowLocation()).Format("02.01.2006 15:04"))
		}
		return e.DB.UpdateGiveawayField(ctx, g.ID, "publish_at", t)

	case StepFinishAt:
		t, err := util.ParseMoscowTime(text)
		if err != nil {
			return err
		}
		// Валидация на шаге: finish_at должен быть позже publish_at (если он задан).
		if g.PublishAt != nil && !t.After(*g.PublishAt) {
			return fmt.Errorf("время итогов должно быть позже публикации (%s)",
				g.PublishAt.In(util.MoscowLocation()).Format("15:04"))
		}
		return e.DB.UpdateGiveawayField(ctx, g.ID, "finish_at", t)

	case StepFinishCount:
		n, err := strconv.Atoi(strings.TrimSpace(text))
		if err != nil || n < 2 || n > 1000000 {
			return errors.New("введите целое число участников (от 2 до 1000000)")
		}
		return e.DB.UpdateGiveawayField(ctx, g.ID, "finish_participants_count", n)

	case StepWinners:
		n, err := strconv.Atoi(strings.TrimSpace(text))
		if err != nil || n < 1 || n > 100 {
			return errors.New("число победителей должно быть целым от 1 до 100")
		}
		return e.DB.UpdateGiveawayField(ctx, g.ID, "winners_count", n)

	case StepWinnersMsg:
		return e.DB.UpdateGiveawayField(ctx, g.ID, "winners_message", text)

	case StepResultsText:
		return e.DB.UpdateGiveawayField(ctx, g.ID, "results_post_text", text)

	// Шаги с выбором каналов / опциями обрабатываются через callback, не текстом.
	case StepMedia, StepSubChannels, StepPubChannels, StepPublishMode, StepFinishMode,
		StepResultsLoc, StepCaptcha, StepPreview:
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
	case StepButtonText:
		// Дефолтный текст кнопки участия.
		_ = e.DB.UpdateGiveawayField(ctx, g.ID, "button_text", "Участвовать")
	case StepWinnersMsg:
		// ничего не пишем — остаётся пустое сообщение
	case StepCaptcha:
		_ = e.DB.UpdateGiveawayField(ctx, g.ID, "captcha_mode", store.CaptchaNone)
	}
	return nil
}
