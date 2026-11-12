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
// Режим «Опубликовать сразу»: publish_at перезаписывается на now ЗДЕСЬ —
// в момент «Принять» (не в момент выбора режима: пока юзер шёл по оставшимся
// шагам, раннее время успевало «пройти» и валидация его отвергала).
func (e *Engine) Confirm(ctx context.Context, id int64) error {
	g, err := e.DB.GetGiveawayAny(ctx, id)
	if err != nil {
		return err
	}
	if g.PublishNow {
		now := time.Now().UTC()
		_ = e.DB.UpdateGiveawayFieldAny(ctx, id, "publish_at", now)
		g.PublishAt = &now
	}
	if err := validate(g); err != nil {
		return err
	}
	return e.DB.PublishDraft(ctx, id)
}

// validate проверяет обязательные поля (финальный гард после пошаговой
// валидации: «Далее →» не пускает дальше с незаполненным шагом).
func validate(g store.Giveaway) error {
	if g.PostText == "" && len(g.MediaTokens) == 0 {
		return &ValidationError{Msg: "не заполнен пост (текст или медиа)"}
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
	if !g.PublishNow && g.PublishAt == nil {
		return &ValidationError{Msg: "не задано время публикации"}
	}
	// Проверка условия завершения в зависимости от режима.
	if g.FinishMode == "count" {
		if g.FinishParticipantsCount < 10 {
			return &ValidationError{Msg: "целевое число участников — от 10"}
		}
	} else {
		// Режим по времени (дефолт).
		if g.FinishAt == nil {
			return &ValidationError{Msg: "не задана дата подведения итогов"}
		}
		if g.PublishAt != nil && !g.FinishAt.After(*g.PublishAt) {
			return &ValidationError{Msg: fmt.Sprintf(
				"дата итогов (%s) должна быть позже даты публикации (%s)",
				g.FinishAt.In(util.MoscowLocation()).Format("02.01 15:04"),
				g.PublishAt.In(util.MoscowLocation()).Format("02.01 15:04"))}
		}
		// Протухшее время (смотрел предпросмотр слишком долго) — на исправление.
		if g.FinishAt.Before(time.Now()) {
			return &ValidationError{Msg: fmt.Sprintf(
				"дата итогов (%s) уже прошла — нажмите «✏️» у пункта «Итоги» и выберите новое время",
				g.FinishAt.In(util.MoscowLocation()).Format("02.01.2006 15:04"))}
		}
	}
	if g.PublishAt != nil && g.PublishAt.Before(time.Now().Add(-1*time.Minute)) && !g.PublishNow {
		return &ValidationError{Msg: fmt.Sprintf(
			"дата публикации (%s) уже прошла. Введите время в будущем",
			g.PublishAt.In(util.MoscowLocation()).Format("02.01.2006 15:04"))}
	}
	if g.WinnersCount < 1 || g.WinnersCount > 100 {
		return &ValidationError{Msg: "число победителей должно быть 1-100"}
	}
	return nil
}
