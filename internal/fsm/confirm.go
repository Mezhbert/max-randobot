package fsm

import (
	"context"
	"fmt"

	"github.com/maxbot/giveawaybot/internal/store"
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
	if g.FinishAt == nil {
		return &ValidationError{Msg: "не задана дата подведения итогов"}
	}
	if !g.FinishAt.After(*g.PublishAt) {
		return &ValidationError{Msg: fmt.Sprintf(
			"дата итогов (%s) должна быть позже даты публикации (%s)",
			g.FinishAt.UTC().Format("2006-01-02 15:04"), g.PublishAt.UTC().Format("2006-01-02 15:04"))}
	}
	if g.WinnersCount < 1 || g.WinnersCount > 100 {
		return &ValidationError{Msg: "число победителей должно быть 1-100"}
	}
	return nil
}
