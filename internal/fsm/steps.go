// Package fsm — конечный автомат цепочки создания розыгрыша.
package fsm

import (
	"time"

	"github.com/maxbot/giveawaybot/internal/store"
)

// Идентификаторы шагов FSM.
const (
	StepPostText    = "post_text"
	StepMedia       = "media"
	StepName        = "name"
	StepButtonText  = "button_text"
	StepSubChannels = "sub_channels"
	StepPubChannels = "pub_channels"
	StepPublishMode = "publish_mode" // выбор: сразу / по времени
	StepPublishAt   = "publish_at"
	StepFinishMode  = "finish_mode" // выбор: по времени / по количеству
	StepFinishAt    = "finish_at"
	StepFinishCount = "finish_count" // кол-во участников для finish_mode=count
	StepWinners     = "winners"
	StepWinnersMsg  = "winners_msg"
	StepReferral    = "referral" // приглашение друзей: +1 билет за каждого
	StepResultsLoc  = "results_loc"
	StepResultsText = "results_text"
	StepCaptcha     = "captcha"
	StepPreview     = "preview"
)

// orderedSteps — упорядоченный список шагов (для переходов вперёд/назад).
// Реферальная система и продвижение убраны (не входят в MVP-сценарий).
var orderedSteps = []string{
	StepName,
	StepPostText,
	StepMedia,
	StepButtonText,
	StepSubChannels,
	StepPubChannels,
	StepPublishMode,
	StepPublishAt,
	StepFinishMode,
	StepFinishAt,
	StepFinishCount,
	StepWinners,
	StepWinnersMsg,
	StepReferral,
	StepResultsLoc,
	StepResultsText,
	StepCaptcha,
	StepPreview,
}

// FirstStep — первый шаг цепочки.
const FirstStep = StepName

// StepSkipped — нужно ли ПРОПУСТИТЬ шаг в зависимости от режима розыгрыша:
//   - publish_at — пропускается при «Опубликовать сразу» (publish_at выставлен
//     на now при выборе режима; шаг уже «выполнен» автоматом);
//   - finish_count — пропускается при режиме «по времени»;
//   - finish_at — пропускается при режиме «по количеству»;
//   - results_text — пропускается при «итоги в исходном посте».
//
// Используется и при движении вперёд, и при «Назад» — чтобы навигация не
// попадала на неактуальные шаги.
func StepSkipped(g store.Giveaway, step string) bool {
	switch step {
	case StepPublishAt:
		// Режим «сразу» (флаг) или publish_at уже наступил (выставлен ранее).
		return g.PublishNow || (g.PublishAt != nil && !g.PublishAt.After(time.Now()))
	case StepFinishCount:
		return g.FinishMode != "count"
	case StepFinishAt:
		return g.FinishMode == "count"
	case StepResultsText:
		return g.ResultsInOriginalPost
	}
	return false
}

// optionalSteps — шаги, которые можно пропустить (кнопка "Пропустить").
var optionalSteps = map[string]bool{
	StepMedia:       true,
	StepButtonText:  true, // дефолт "Участвовать"
	StepWinnersMsg:  true,
	StepResultsText: true, // дефолтный текст итогов при пустом
	StepCaptcha:     true,
}

// IsOptional — можно ли пропустить шаг.
func IsOptional(step string) bool { return optionalSteps[step] }

// buttonSteps — шаги, где ввод делается КНОПКАМИ (не текстом). Используется
// для валидации: текст/форварды на таких шагах отклоняются с подсказкой.
var buttonSteps = map[string]bool{
	StepReferral:    true,
	StepSubChannels: true,
	StepPubChannels: true,
	StepPublishMode: true,
	StepFinishMode:  true,
	StepResultsLoc:  true,
	StepCaptcha:     true,
	StepPreview:     true,
}

// IsButtonStep — выбор на этом шаге делается кнопками?
func IsButtonStep(step string) bool { return buttonSteps[step] }

// NextStep возвращает следующий шаг или "" если это конец.
func NextStep(step string) string {
	for i, s := range orderedSteps {
		if s == step && i+1 < len(orderedSteps) {
			return orderedSteps[i+1]
		}
	}
	return ""
}

// PrevStep возвращает предыдущий шаг или "" если это начало.
func PrevStep(step string) string {
	for i, s := range orderedSteps {
		if s == step && i > 0 {
			return orderedSteps[i-1]
		}
	}
	return ""
}

// StepNumber — номер шага в порядке orderedSteps (1-based; 0 — неизвестный).
func StepNumber(step string) int {
	for i, s := range orderedSteps {
		if s == step {
			return i + 1
		}
	}
	return 0
}

// TotalSteps — всего шагов в цепочке.
func TotalSteps() int { return len(orderedSteps) }
