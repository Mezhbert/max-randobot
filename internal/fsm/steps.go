// Package fsm — конечный автомат цепочки создания розыгрыша (шаги 1-13 по ТЗ).
//
// Шаги:
//   step_post_text    (1)   текст поста            [обязательный]
//   step_media        (2)   медиа (фото/видео)      [опциональный, можно пропустить]
//   step_name         (3)   название конкурса        [обязательный]
//   step_button_text  (4)   текст кнопки "участвовать" [обязательный, дефолт "участвовать"]
//   step_sub_channels (5)   каналы для подписки      [обязательный; можно без каналов]
//   step_pub_channels (6)   каналы публикации        [обязательный]
//   step_publish_at   (7)   дата публикации          [обязательный, ДД.ММ.ГГГГ ЧЧ:ММ, МСК]
//   step_finish_at    (8)   дата итогов              [обязательный, ДД.ММ.ГГГГ ЧЧ:ММ, МСК]
//   step_winners      (9)   кол-во победителей (1-100) [обязательный]
//   step_winners_msg  (10)  сообщение победителям    [опциональный]
//   step_results_loc  (10.5) где публиковать итоги   [обязательный]
//   step_results_text (10.6) текст итогов отдельного поста [опц., только при «отдельным постом»]
//   step_referral     (11)  реферальная система      [опциональный]
//   step_captcha      (12)  капча                    [опциональный]
//   step_promotion    (13)  продвижение              [опциональный]
//   step_preview              предпросмотр + Принять/Отклонить
package fsm

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
	StepResultsLoc  = "results_loc"
	StepResultsText = "results_text"
	StepReferral    = "referral"
	StepCaptcha     = "captcha"
	StepPromotion   = "promotion"
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
	StepResultsLoc,
	StepResultsText,
	StepCaptcha,
	StepPreview,
}

// FirstStep — первый шаг цепочки.
const FirstStep = StepName

// SkipStepAfter — шаги, которые ПРОПУСКАЮТСЯ в зависимости от выбора режима:
// publish_now → пропустить publish_at; finish_time → пропустить finish_count;
// finish_count → пропустить finish_at. Используется advanceSkipping.
var conditionalSteps = map[string]bool{
	StepPublishAt:   true, // пропускается при «Опубликовать сразу»
	StepFinishCount: true, // пропускается при «По времени»
	StepFinishAt:    true, // пропускается при «По количеству»
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
