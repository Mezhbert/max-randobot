// Package fsm — конечный автомат цепочки создания розыгрыша.
// Интерфейс по мануалу RandoMaster_MAX (dev-notes/Claude-log): 10 шагов,
// «Один шаг — одно сообщение», прогресс-бар, первый вариант предвыбран «✓».
package fsm

import (
	"github.com/maxbot/giveawaybot/internal/store"
)

// Идентификаторы шагов FSM (порядок = orderedSteps, = мануал §4).
const (
	StepPost    = "post"    // 1: текст и/или медиа одним сообщением
	StepChannel = "channel" // 2: канал публикации
	StepName    = "name"    // 3: название (авто-варианты)
	StepSubs    = "subs"    // 4: каналы подписки
	StepButton  = "button"  // 5: текст кнопки участия
	StepWinners = "winners" // 6: количество победителей
	StepFinish  = "finish"  // 7: итоги (быстрые даты / календарь / по числу)
	StepStart   = "start"   // 8: старт (сразу / час / завтра / дата)
	StepExtras  = "extras"  // 9: капча / друзья / сообщение победителю / место итогов
	StepCheck   = "check"   // 10: проверка (превью + сводка + правка пунктов)
)

// orderedSteps — упорядоченный список шагов (для переходов вперёд/назад).
var orderedSteps = []string{
	StepPost,
	StepChannel,
	StepName,
	StepSubs,
	StepButton,
	StepWinners,
	StepFinish,
	StepStart,
	StepExtras,
	StepCheck,
}

// FirstStep — первый шаг цепочки.
const FirstStep = StepPost

// stepMeta — эмодзи и заголовок шага (для строки «Шаг N из 10 · Заголовок»).
var stepMeta = map[string][2]string{
	StepPost:    {"📝", "Пост"},
	StepChannel: {"📣", "Канал"},
	StepName:    {"🏷", "Название"},
	StepSubs:    {"📢", "Подписки"},
	StepButton:  {"🔘", "Кнопка"},
	StepWinners: {"🏆", "Победители"},
	StepFinish:  {"🏁", "Итоги"},
	StepStart:   {"🚀", "Старт"},
	StepExtras:  {"⚙️", "Доп. настройки"},
	StepCheck:   {"✅", "Проверка"},
}

// StepEmoji/StepTitle — метаданные шага для рендера.
func StepEmoji(step string) string { return stepMeta[step][0] }
func StepTitle(step string) string { return stepMeta[step][1] }

// StepSkipped — условный пропуск шага. В 10-шаговой схеме мануала условных
// шагов нет (даты/календарь/счёт — подэкраны своих шагов), поэтому всегда
// false. Функция сохранена: GoBack/GoNext используют её, а старые черновики
// из БД с legacy-шагами сбрасываются на FirstStep в ResumeDraft.
func StepSkipped(g store.Giveaway, step string) bool { return false }

// optionalSteps — шаги, которые можно пропустить (в новой схеме «Далее →»
// сам валидирует заполненность; пропуск как кнопка не нужен).
var optionalSteps = map[string]bool{}

// IsOptional — можно ли пропустить шаг.
func IsOptional(step string) bool { return optionalSteps[step] }

// buttonSteps — шаги, где ввод делается ТОЛЬКО кнопками (текст отклоняется
// с подсказкой). У остальных шагов текст валиден («✍️ …»).
var buttonSteps = map[string]bool{
	StepSubs:  true, // выбор каналов тогглами
	StepCheck: true, // правка пунктов/публикация
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

// ProgressBar — «▰▰▰▰▰▰▱▱▱▱»: n заполненных из total.
func ProgressBar(n, total int) string {
	if n > total {
		n = total
	}
	if n < 0 {
		n = 0
	}
	bar := ""
	for i := 0; i < n; i++ {
		bar += "▰"
	}
	for i := n; i < total; i++ {
		bar += "▱"
	}
	return bar
}
