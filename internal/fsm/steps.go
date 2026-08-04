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
	StepPublishAt   = "publish_at"
	StepFinishAt    = "finish_at"
	StepWinners     = "winners"
	StepWinnersMsg  = "winners_msg"
	StepResultsLoc  = "results_loc"
	StepReferral    = "referral"
	StepCaptcha     = "captcha"
	StepPromotion   = "promotion"
	StepPreview     = "preview"
)

// orderedSteps — упорядоченный список шагов (для переходов вперёд/назад).
var orderedSteps = []string{
	StepPostText,
	StepMedia,
	StepName,
	StepButtonText,
	StepSubChannels,
	StepPubChannels,
	StepPublishAt,
	StepFinishAt,
	StepWinners,
	StepWinnersMsg,
	StepResultsLoc,
	StepReferral,
	StepCaptcha,
	StepPromotion,
	StepPreview,
}

// FirstStep — первый шаг цепочки.
const FirstStep = StepPostText

// optionalSteps — шаги, которые можно пропустить (кнопка "Пропустить").
var optionalSteps = map[string]bool{
	StepMedia:      true,
	StepButtonText: true, // дефолт "Участвовать"
	StepWinnersMsg: true,
	StepReferral:   true,
	StepCaptcha:    true,
	StepPromotion:  true,
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
