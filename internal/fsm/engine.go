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
	case StepPostText:
		return "📝 Шаг 1/13. Текст поста.\n\nОтправьте текст поста розыгрыша. " +
			"Оформление сохранится точно в таком же виде, как вы отправите.\n\n" +
			"Если в тексте будут ссылки на каналы — подпишитесь на них участники должны будут обязательно."
	case StepMedia:
		return "🖼 Шаг 2/13. Медиа (опционально).\n\nОтправьте фото и/или видео для поста, либо нажмите «Пропустить»."
	case StepName:
		return "🏷 Шаг 3/13. Название конкурса.\n\nОтправьте текстом название конкурса. " +
			"Оно используется только как имя конкурса (в самом посте не появится)."
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
	case StepPublishAt:
		return "📅 Шаг 7/13. Дата публикации.\n\nБот живёт по московскому времени.\n" +
			"Введите дату и время в формате: ДД.ММ.ГГГГ ЧЧ:ММ\n" +
			"Пример: 31.12.2026 18:00"
	case StepFinishAt:
		return "🏁 Шаг 8/13. Дата подведения итогов.\n\nБот живёт по московскому времени.\n" +
			"Введите дату и время в формате: ДД.ММ.ГГГГ ЧЧ:ММ\n" +
			"Пример: 07.01.2027 21:00"
	case StepWinners:
		return "🏆 Шаг 9/13. Количество победителей.\n\nВведите число от 1 до 100."
	case StepWinnersMsg:
		return "✉️ Шаг 10/13. Сообщение победителям (опционально).\n\n" +
			"Отправьте текст сообщения, которое бот пришлёт победителям в личку " +
			"(например, контакты организаторов для получения приза).\n" +
			"Или нажмите «Пропустить»."
	case StepResultsLoc:
		return "📍 Шаг 10.5/13. Где публиковать итоги.\n\n" +
			"• «В исходном посте» — итоги появятся в посте розыгрыша.\n" +
			"• «Отдельным постом» — нужно будет отправить текст поста с итогами."
	case StepReferral:
		return "👥 Шаг 11/13. Реферальная система (опционально).\n\n" +
			"Хотите включить реферальную систему?\n\nКак работает:\n" +
			"• Каждый участник получает реф-ссылку\n" +
			"• За каждого приглашённого — +1 билет\n" +
			"• Больше билетов = выше шансы победить\n" +
			"(лимит приглашений по умолчанию — 10)"
	case StepCaptcha:
		return "🤖 Шаг 12/13. Капча (опционально).\n\nВключить проверку на человека?\n" +
			"• Нет\n• Для всех\n• Для процента участников"
	case StepPromotion:
		return "🚀 Шаг 13/13. Продвижение (опционально, бесплатно).\n\n" +
			"Включить продвижение? Розыгрыш попадёт в список для платных подписчиков бота, " +
			"им придёт уведомление о вашем розыгрыше."
	case StepPreview:
		return "👁 Предпросмотр розыгрыша. Проверьте всё ли верно."
	}
	return ""
}

// StartDraft начинает новый черновик или возвращает существующий (один на пользователя).
func (e *Engine) StartDraft(ctx context.Context, ownerUserID int64) (store.Giveaway, error) {
	// Есть ли уже активный черновик в режиме создания?
	if g, err := e.DB.GetDraftGiveaway(ctx, ownerUserID); err == nil {
		return g, nil
	}
	return e.DB.CreateDraftGiveaway(ctx, ownerUserID, FirstStep)
}

// Abandon закрывает создание (кнопка Отменить) — черновик сохраняется, выход в меню.
func (e *Engine) Abandon(ctx context.Context, id int64) error {
	return e.DB.AbandonDraft(ctx, id)
}

// GoBack откатывает на предыдущий шаг.
func (e *Engine) GoBack(ctx context.Context, id int64, currentStep string) (string, error) {
	prev := PrevStep(currentStep)
	if prev == "" {
		return currentStep, nil
	}
	if err := e.DB.SetDraftStep(ctx, id, prev); err != nil {
		return currentStep, err
	}
	return prev, nil
}

// GoNext подтверждает шаг и переходит к следующему.
func (e *Engine) GoNext(ctx context.Context, id int64, currentStep string) (string, error) {
	next := NextStep(currentStep)
	if next == "" {
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
	} else if g.CaptchaMode == "percent" {
		captcha = fmt.Sprintf("для %d%% участников", g.CaptchaPercent)
	}

	return "Информация о розыгрыше:\n" +
		fmt.Sprintf("Название: %s\n", nonEmpty(g.Name)) +
		fmt.Sprintf("Дата начала: %s\n", util.FormatMoscowTime(safeTime(g.PublishAt))) +
		fmt.Sprintf("Дата окончания: %s\n", util.FormatMoscowTime(safeTime(g.FinishAt))) +
		fmt.Sprintf("Кол-во победителей: %d\n", g.WinnersCount) +
		fmt.Sprintf("Контакт для победителей: %s\n", nonEmpty(g.WinnersMessage)) +
		fmt.Sprintf("Результаты в исходных постах: %s\n", yesno(g.ResultsInOriginalPost)) +
		fmt.Sprintf("Буст каналов: %s\n", yesno(len(g.SubscribeChannelIDs) > 0)) +
		fmt.Sprintf("Приглашение друзей: %s\n", yesno(g.ReferralEnabled)) +
		fmt.Sprintf("Капча: %s\n", captcha) +
		fmt.Sprintf("Продвижение от бота: %s\n", yesno(g.PromotionEnabled)) +
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
