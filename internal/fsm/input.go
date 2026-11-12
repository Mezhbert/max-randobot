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

// HandleTextInput обрабатывает текстовый ввод пользователя для текущего шага
// (мануал §3: «Валидный → сразу следующий шаг; невалидный → тот же шаг с
// первой строкой ❌»). Возвращает ошибку валидации (nil = успех, переход).
//
// Шаг 1 (пост) текстом не обрабатывается — ему нужны и вложения, см.
// handlers.handlePostInput.
func (e *Engine) HandleTextInput(ctx context.Context, g store.Giveaway, text string) error {
	switch g.DraftStep {
	case StepName:
		if err := util.ValidateName(text); err != nil {
			return err
		}
		return e.DB.UpdateGiveawayField(ctx, g.ID, "name", text)

	case StepButton:
		if err := util.ValidateButtonText(text); err != nil {
			return err
		}
		return e.DB.UpdateGiveawayField(ctx, g.ID, "button_text", text)

	case StepWinners:
		n, err := strconv.Atoi(strings.TrimSpace(text))
		if err != nil || n < 1 || n > 100 {
			return errors.New("число победителей должно быть целым от 1 до 100")
		}
		return e.DB.UpdateGiveawayField(ctx, g.ID, "winners_count", n)

	case StepFinish:
		// Режим «по числу» → ввод цели; иначе — дата итогов.
		if g.FinishMode == "count" {
			n, err := strconv.Atoi(strings.TrimSpace(text))
			if err != nil || n < 10 || n > 1000000 {
				return errors.New("введите целевое число участников (от 10 до 1000000)")
			}
			return e.DB.UpdateGiveawayField(ctx, g.ID, "finish_participants_count", n)
		}
		t, err := util.ParseMoscowTime(text)
		if err != nil {
			return err
		}
		if t.Before(time.Now().Add(10 * time.Minute)) {
			return fmt.Errorf("итоги в %s — слишком рано, участники не успеют. Введите не раньше %s",
				t.In(util.MoscowLocation()).Format("15:04"),
				time.Now().Add(10*time.Minute).In(util.MoscowLocation()).Format("02.01 15:04"))
		}
		if g.PublishAt != nil && !g.PublishNow && !t.After(*g.PublishAt) {
			return fmt.Errorf("итоги должны быть позже публикации (%s)",
				g.PublishAt.In(util.MoscowLocation()).Format("02.01 15:04"))
		}
		return e.DB.UpdateGiveawayField(ctx, g.ID, "finish_at", t)

	case StepStart:
		t, err := util.ParseMoscowTime(text)
		if err != nil {
			return err
		}
		if !t.After(time.Now()) {
			return fmt.Errorf("время публикации %s уже прошло — введите будущее время",
				t.In(util.MoscowLocation()).Format("02.01.2006 15:04"))
		}
		if g.FinishMode != "count" && g.FinishAt != nil && !t.Before(*g.FinishAt) {
			return fmt.Errorf("публикация должна быть раньше итогов (%s)",
				g.FinishAt.In(util.MoscowLocation()).Format("02.01 15:04"))
		}
		if err := e.DB.UpdateGiveawayField(ctx, g.ID, "publish_now", false); err != nil {
			return err
		}
		return e.DB.UpdateGiveawayField(ctx, g.ID, "publish_at", t)

	case StepExtras:
		// Число 1–10 = лимит друзей (мануал §7: «или отправьте своё число»).
		if g.ReferralEnabled {
			n, err := strconv.Atoi(strings.TrimSpace(text))
			if err != nil || n < 1 || n > 10 {
				return errors.New("на этом шаге текст — лимит друзей: целое число 1–10")
			}
			return e.DB.UpdateGiveawayField(ctx, g.ID, "referral_limit", n)
		}
		return errors.New("сначала включите «👥 Друзья», чтобы задать лимит числом")

	// Шаги только с кнопками: подписки (тогглы) и проверка.
	case StepSubs, StepCheck, StepChannel:
		// Канал: текст/форвард обрабатывается в handlers (добавление канала).
		return errors.New("используйте кнопки под сообщением")
	}
	return errors.New("неизвестный шаг")
}
