package fsm

import (
	"context"
	"fmt"
	"log/slog"
	"strings"
	"time"

	"github.com/maxbot/giveawaybot/internal/store"
	"github.com/maxbot/giveawaybot/internal/util"
)

// Engine хранит зависимости, общие для всех обработчиков шагов.
type Engine struct {
	DB  *store.Store
	log *slog.Logger
}

// New создаёт движок FSM.
func New(db *store.Store, log *slog.Logger) *Engine {
	if log == nil {
		log = slog.Default()
	}
	return &Engine{DB: db, log: log}
}

// StepPrompt описывает, что показать пользователю на шаге.
type StepPrompt struct {
	Text       string // текст запроса
	Attachment string // json-ignored; используем Keyboard ниже
	Keyboard   []byte // зарезервировано
}

// PromptForStep возвращает текст-приглашение для шага.
func PromptForStep(step string) string {
	switch step {
	case StepName:
		return "🏷 Шаг 1/13. Название конкурса.\n\nОтправьте текстом название конкурса. " +
			"Оно используется как имя конкурса (в списке «Мои розыгрыши»), в самом посте не появится.\n\n" +
			"Черновик сохраняется автоматически после ввода названия."
	case StepPostText:
		return "📝 Шаг 2/13. Текст поста.\n\nОтправьте текст поста розыгрыша. " +
			"Оформление сохранится точно в таком же виде, как вы отправите.\n\n" +
			"Если в тексте будут ссылки на каналы — подпишитесь на них участники должны будут обязательно."
	case StepMedia:
		return "🖼 Шаг 3/13. Медиа (опционально).\n\nОтправьте фото и/или видео для поста, либо нажмите «Пропустить»."
	case StepButtonText:
		return "🔘 Шаг 4/13. Текст кнопки участия.\n\nОтправьте текст кнопки (по умолчанию «участвовать»)."
	case StepSubChannels:
		return "📢 Шаг 5/13. Каналы для подписки.\n\nВыберите каналы, на которые участникам нужно подписаться.\n" +
			"Если каналы не выбраны — розыгрыш проходит без проверки подписки.\n\n" +
			"Каналы появляются здесь автоматически, когда вы добавляете бота админом в канал.\n" +
			"⚠️ рядом с каналом — бот больше не админ (использовать нельзя).\n\n" +
			"Когда закончите выбор — нажмите «Далее»."
	case StepPubChannels:
		return "📣 Шаг 6/13. Каналы публикации.\n\nВыберите каналы, где опубликовать розыгрыш. " +
			"Бот может опубликовать в любом канале, где он админ.\n\n" +
			"Выбирайте кнопками из списка. Когда закончите — нажмите «Далее»."
	case StepPublishMode:
		return "🚀 Шаг 7/13. Когда опубликовать?\n\n" +
			"• «Опубликовать сразу» — пост выйдет через ~минуту после создания\n" +
			"• «По времени» — задать дату и время публикации"
	case StepPublishAt:
		return "📅 Шаг 8/13. Дата публикации.\n\nБот живёт по московскому времени.\n" +
			"Введите время в формате ЧЧ:ММ (например 18:00) — дата подставится автоматически.\n" +
			"Или полный формат: ДД.ММ.ГГГГ ЧЧ:ММ (например 31.12.2026 18:00)"
	case StepFinishMode:
		return "🏁 Шаг 9/13. Как подвести итоги?\n\n" +
			"• «По времени» — итоги в заданную дату/время\n" +
			"• «По количеству участников» — как только наберётся заданное число участников"
	case StepFinishAt:
		return "⏰ Шаг 10/13. Дата подведения итогов.\n\nБот живёт по московскому времени.\n" +
			"Введите время в формате ЧЧ:ММ (например 21:00) — дата подставится автоматически.\n" +
			"Или полный формат: ДД.ММ.ГГГГ ЧЧ:ММ (например 07.01.2027 21:00)"
	case StepFinishCount:
		return "👥 Шаг 10/13. Целевое число участников.\n\nВведите число — розыгрыш завершится автоматически, " +
			"как только участников станет столько (например 100)."
	case StepWinners:
		return "🏆 Шаг 11/13. Количество победителей.\n\nВведите число от 1 до 100."
	case StepWinnersMsg:
		return "✉️ Шаг 12/13. Сообщение победителям (опционально).\n\n" +
			"Отправьте текст сообщения, которое бот пришлёт победителям в личку " +
			"(например, контакты организаторов для получения приза).\n" +
			"Или нажмите «Пропустить»."
	case StepResultsLoc:
		return "📍 Шаг 13/13. Где публиковать итоги.\n\n" +
			"• «В исходном посте» — итоги появятся в посте розыгрыша.\n" +
			"• «Отдельным постом» — нужно будет отправить текст поста с итогами."
	case StepResultsText:
		return "📝 Текст поста с итогами.\n\n" +
			"Отправьте текст для отдельного поста с итогами розыгрыша.\n" +
			"Или нажмите «Пропустить» — будет использован стандартный заголовок «🏁 Итоги розыгрыша»."
	case StepCaptcha:
		return "🤖 Капча.\n\nВключить проверку на человека (капча при участии)?\n" +
			"✅ Да — все участники решают капчу\n❌ Нет — без проверки"
	case StepPreview:
		return "👁 Предпросмотр розыгрыша. Проверьте всё ли верно."
	}
	return ""
}

// HintForStep — краткая подсказка для шага (кнопка 💡). Одна-две строки,
// показывается через callback notification.
func HintForStep(step string) string {
	switch step {
	case StepName:
		return "Название видно только вам в списке «Мои розыгрыши». Например: «iPhone 15 в мае»."
	case StepPostText:
		return "Это текст поста в канале. Можно копировать из готового описания. Оформление сохранится как есть."
	case StepMedia:
		return "Пришлите фото/видео одним сообщением. Можно несколько — каждое отдельным сообщением, затем «Пропустить»."
	case StepButtonText:
		return "Текст на кнопке под постом, например «Участвовать». Пусто или «Пропустить» = стандартная кнопка."
	case StepSubChannels:
		return "Каналы, куда участники ОБЯЗАНЫ подписаться. Можно не выбирать — тогда без проверки подписки."
	case StepPubChannels:
		return "Каналы, где ВЫЛОЖИТСЯ пост розыгрыша. ⚠️ = бот не админ, выбрать нельзя."
	case StepPublishMode:
		return "«Сразу» — через минуту после подтверждения. «По времени» — зададите дату и время."
	case StepPublishAt:
		return "Достаточно ввести ЧЧ:ММ — дата подставится сама (сегодня или завтра). Полный формат: ДД.ММ.ГГГГ ЧЧ:ММ."
	case StepFinishMode:
		return "«По времени» — фиксированная дата итогов. «По количеству» — розыгрыш закроется при наборе N участников."
	case StepFinishAt:
		return "Когда подвести итоги. Достаточно ЧЧ:ММ. Должно быть позже публикации."
	case StepFinishCount:
		return "Розыгрыш завершится автоматически при достижении этого числа участников. Например 100."
	case StepWinners:
		return "Сколько победителей выбрать. От 1 до 100."
	case StepWinnersMsg:
		return "Личное сообщение победителям (контакты для приза). Можно пропустить."
	case StepResultsLoc:
		return "«В исходном посте» — итоги допишутся в конец поста. «Отдельным постом» — новый пост с итогами."
	case StepResultsText:
		return "Текст отдельного поста с итогами. Пропустить = стандартный заголовок."
	case StepCaptcha:
		return "Капча — картинка с цифрами в чате с ботом. Отсекает простых ботов."
	case StepPreview:
		return "Финальная проверка. «Принять» — розыгрыш запланирован и будет опубликован автоматически."
	}
	return ""
}

// StartDraft создаёт НОВЫЙ черновик. Старые пустые (без названия) чистятся.
// Для продолжения существующего черновика используется ResumeDraft (кнопка
// «Продолжить создание» в карточке).
func (e *Engine) StartDraft(ctx context.Context, ownerUserID int64) (store.Giveaway, error) {
	_ = e.DB.DeleteEmptyDrafts(ctx, ownerUserID)
	return e.DB.CreateDraftGiveaway(ctx, ownerUserID, FirstStep)
}

// ResumeDraft возобновляет создание существующего черновика по id.
// Используется кнопкой «Продолжить создание» в карточке черновика.
// Если шаг создания пуст (черновик сохранён через «Отменить») — стартуем с первого шага.
func (e *Engine) ResumeDraft(ctx context.Context, id int64) (store.Giveaway, error) {
	g, err := e.DB.GetGiveawayAny(ctx, id)
	if err != nil {
		return g, err
	}
	if g.Status != store.StatusDraft {
		return g, fmt.Errorf("это не черновик")
	}
	step := g.DraftStep
	if step == "" {
		step = FirstStep
		if err := e.DB.SetDraftStep(ctx, id, step); err != nil {
			return g, err
		}
		g.DraftStep = step
	}
	return g, nil
}

// Abandon закрывает создание (кнопка Отменить) — черновик сохраняется, выход в меню.
func (e *Engine) Abandon(ctx context.Context, id int64) error {
	return e.DB.AbandonDraft(ctx, id)
}

// GoBack откатывает на предыдший АКТУАЛЬНЫЙ шаг (условные шаги, не подходящие
// по режиму розыгрыша, перескакиваются — «Назад» не должен попадать на шаг,
// который был пропущен при движении вперёд).
func (e *Engine) GoBack(ctx context.Context, id int64, currentStep string, g store.Giveaway) (string, error) {
	prev := currentStep
	for {
		p := PrevStep(prev)
		if p == "" {
			return currentStep, nil // достигнуто начало — остаёмся
		}
		prev = p
		if !StepSkipped(g, prev) {
			break
		}
	}
	if err := e.DB.SetDraftStep(ctx, id, prev); err != nil {
		return currentStep, err
	}
	return prev, nil
}

// GoNext подтверждает шаг и переходит к следующему АКТУАЛЬНОМУ шагу
// (условные шаги перескакиваются по режиму розыгрыша).
func (e *Engine) GoNext(ctx context.Context, id int64, currentStep string, g store.Giveaway) (string, error) {
	next := currentStep
	for {
		n := NextStep(next)
		if n == "" {
			return currentStep, nil
		}
		next = n
		if !StepSkipped(g, next) {
			break
		}
	}
	if next == currentStep {
		return currentStep, nil
	}
	if err := e.DB.SetDraftStep(ctx, id, next); err != nil {
		return currentStep, err
	}
	return next, nil
}

// FormatPreview строит подробную сводку о розыгрыше для подтверждения.
func FormatPreview(g store.Giveaway, db *store.Store) string {
	subs := channelNames(db, g.SubscribeChannelIDs, g.OwnerUserID)
	pubs := channelNames(db, g.PublishChannelIDs, g.OwnerUserID)
	resultsLoc := "в исходном посте"
	if !g.ResultsInOriginalPost {
		resultsLoc = "отдельным постом"
	}
	captcha := "нет"
	if g.CaptchaMode == "all" {
		captcha = "для всех"
	}

	return "Информация о розыгрыше:\n" +
		fmt.Sprintf("Название: %s\n", nonEmpty(g.Name)) +
		fmt.Sprintf("Дата начала: %s\n", util.FormatMoscowTime(safeTime(g.PublishAt))) +
		fmt.Sprintf("Дата окончания: %s\n", util.FormatMoscowTime(safeTime(g.FinishAt))) +
		fmt.Sprintf("Кол-во победителей: %d\n", g.WinnersCount) +
		fmt.Sprintf("Контакт для победителей: %s\n", nonEmpty(g.WinnersMessage)) +
		fmt.Sprintf("Результаты в исходных постах: %s\n", yesno(g.ResultsInOriginalPost)) +
		fmt.Sprintf("Буст каналов: %s\n", yesno(len(g.SubscribeChannelIDs) > 0)) +
		fmt.Sprintf("Капча: %s\n", captcha) +
		"\nКаналы для подписки:\n" + subs + "\n" +
		"Старт будет опубликован в:\n" + pubs + "\n" +
		"Итоги будут опубликованы: " + resultsLoc
}

func nonEmpty(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

func yesno(b bool) string {
	if b {
		return "да"
	}
	return "нет"
}

// safeTime разыменовывает указатель, возвращая zero time если nil.
func safeTime(t *time.Time) time.Time {
	if t == nil {
		return time.Time{}
	}
	return *t
}

// channelNames возвращает текстовый список каналов по их внутренним id.
func channelNames(db *store.Store, ids []int64, ownerUserID int64) string {
	if len(ids) == 0 {
		return "—"
	}
	channels, err := db.ChannelsByIDs(context.Background(), ownerUserID, ids)
	if err != nil || len(channels) == 0 {
		return "—"
	}
	lines := make([]string, 0, len(channels))
	for _, c := range channels {
		title := c.Title
		if title == "" {
			title = fmt.Sprintf("канал #%d", c.ChatID)
		}
		lines = append(lines, "• "+title)
	}
	return strings.Join(lines, "\n")
}
