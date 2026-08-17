package fsm

import (
	"context"
	"fmt"
	"time"

	"github.com/maxbot/giveawaybot/internal/store"
	"github.com/maxbot/giveawaybot/internal/util"
)

// ConfirmPublish — пользователь нажал "Принять" на предпросмотре.
// Валидирует обязательные поля и переводит черновик в scheduled/active.
type ValidationError struct{ Msg string }

func (e *ValidationError) Error() string { return e.Msg }

// Confirm валидирует черновик и публикует его (scheduled/active).
func (e *Engine) Confirm(ctx context.Context, id int64) error {
	g, err := e.DB.GetGiveawayAny(ctx, id)
	if err != nil {
		return err
	}
	if err := validate(g); err != nil {
		return err
	}
	return e.DB.PublishDraft(ctx, id)
}

// validate проверяет обязательные поля по ТЗ.
// Итоги: finish_mode='time' требует FinishAt, 'count' требует FinishParticipantsCount.
func validate(g store.Giveaway) error {
	if g.PostText == "" {
		return &ValidationError{Msg: "не заполнен текст поста"}
	}
	if g.Name == "" {
		return &ValidationError{Msg: "не заполнено название конкурса"}
	}
	if g.ButtonText == "" {
		return &ValidationError{Msg: "не задан текст кнопки участия"}
	}
	if len(g.PublishChannelIDs) == 0 {
		return &ValidationError{Msg: "не выбраны каналы публикации"}
	}
	if g.PublishAt == nil {
		return &ValidationError{Msg: "не задана дата публикации"}
	}
	// Проверка условия завершения в зависимости от режима.
	if g.FinishMode == "count" {
		if g.FinishParticipantsCount < 2 {
			return &ValidationError{Msg: "не задано целевое число участников"}
		}
	} else {
		// Режим по времени (дефолт).
		if g.FinishAt == nil {
			return &ValidationError{Msg: "не задана дата подведения итогов"}
		}
		if !g.FinishAt.After(*g.PublishAt) {
			return &ValidationError{Msg: fmt.Sprintf(
				"дата итогов (%s) должна быть позже даты публикации (%s)",
				g.FinishAt.UTC().Format("2006-01-02 15:04"), g.PublishAt.UTC().Format("2006-01-02 15:04"))}
		}
	}
	// Защита от случайного ввода прошедшего времени: публикация «в прошлом»
	// сработала бы мгновенно (статус active), что выглядит как баг для юзера.
	// Допускаем небольшой запас (1 мин) на случай медленной обработки.
	if g.PublishAt.Before(time.Now().Add(-1 * time.Minute)) {
		return &ValidationError{Msg: fmt.Sprintf(
			"дата публикации (%s) уже прошла. Введите время в будущем",
			g.PublishAt.In(util.MoscowLocation()).Format("02.01.2006 15:04"))}
	}
	if g.WinnersCount < 1 || g.WinnersCount > 100 {
		return &ValidationError{Msg: "число победителей должно быть 1-100"}
	}
	return nil
}
