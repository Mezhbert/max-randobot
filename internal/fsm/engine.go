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

// legacyStepMap — шаги старого 18-шагового мастера → ближайший шаг новой
// 10-шаговой схемы: черновики из БД продолжаются с осмысленного места.
var legacyStepMap = map[string]string{
	"post_text": StepPost, "media": StepPost,
	"sub_channels": StepSubs, "pub_channels": StepChannel,
	"button_text": StepButton,
	"publish_mode": StepStart, "publish_at": StepStart,
	"finish_mode": StepFinish, "finish_at": StepFinish, "finish_count": StepFinish,
	"winners": StepWinners, "winners_msg": StepExtras,
	"referral": StepExtras, "results_loc": StepExtras, "results_text": StepExtras,
	"captcha": StepExtras, "preview": StepCheck,
}

// NormalizeStep — валидный шаг новой схемы для значения из БД (legacy →
// новый, неизвестный → FirstStep).
func NormalizeStep(step string) string {
	if StepNumber(step) > 0 {
		return step
	}
	if s, ok := legacyStepMap[step]; ok {
		return s
	}
	return FirstStep
}

// stepKnown — существует ли шаг в новой схеме.
func stepKnown(step string) bool { return StepNumber(step) > 0 }

// StartDraft создаёт НОВЫЙ черновик с дефолтами мануала: первый вариант
// каждого шага уже выбран (капча вкл, победитель 1, итоги завтра 21:00,
// старт сразу) — мастер проходится нажатиями «Далее →».
func (e *Engine) StartDraft(ctx context.Context, ownerUserID int64) (store.Giveaway, error) {
	_ = e.DB.DeleteEmptyDrafts(ctx, ownerUserID)
	g, err := e.DB.CreateDraftGiveaway(ctx, ownerUserID, FirstStep)
	if err != nil {
		return g, err
	}
	// Дефолты мануала (§4 «По умолчанию (первый)»).
	tomorrow21 := time.Now().In(util.MoscowLocation()).AddDate(0, 0, 1)
	tomorrow21 = time.Date(tomorrow21.Year(), tomorrow21.Month(), tomorrow21.Day(), 21, 0, 0, 0, util.MoscowLocation()).UTC()
	defaults := []struct {
		field string
		val   any
	}{
		{"button_text", "Участвовать"},
		{"winners_count", 1},
		{"captcha_mode", store.CaptchaAll},
		{"finish_mode", "time"},
		{"finish_at", tomorrow21},
		{"publish_now", true},
		{"results_in_original_post", true},
		{"referral_enabled", false},
	}
	for _, d := range defaults {
		if err := e.DB.UpdateGiveawayField(ctx, g.ID, d.field, d.val); err != nil {
			e.log.Debug("start draft default", "err", err, "field", d.field)
		}
	}
	// Дефолты из последнего созданного розыгрыша (каналы, капча, кнопка,
	// рефералка) — повторяющиеся розыгрыши собираются быстрее. Поверх
	// дефолтов мануала: опыт владельца важнее.
	if err := e.DB.ApplyLastGiveawayDefaults(ctx, ownerUserID, g.ID); err != nil {
		e.log.Debug("apply defaults", "err", err)
	}
	gg, err := e.DB.GetGiveawayAny(ctx, g.ID)
	if err == nil {
		return gg, nil
	}
	return g, nil
}

// ResumeDraft возобновляет создание существующего черновика по id.
// Пустой/legacy шаг нормализуется к новой 10-шаговой схеме.
func (e *Engine) ResumeDraft(ctx context.Context, id int64) (store.Giveaway, error) {
	g, err := e.DB.GetGiveawayAny(ctx, id)
	if err != nil {
		return g, err
	}
	if g.Status != store.StatusDraft {
		return g, fmt.Errorf("это не черновик")
	}
	step := NormalizeStep(g.DraftStep)
	if step != g.DraftStep {
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

// GoBack откатывает на предыдущий шаг (условных шагов в новой схеме нет).
func (e *Engine) GoBack(ctx context.Context, id int64, currentStep string, g store.Giveaway) (string, error) {
	prev := PrevStep(currentStep)
	if prev == "" {
		return currentStep, nil // первый шаг — остаёмся (в меню ведёт «✖ Отменить»)
	}
	if err := e.DB.SetDraftStep(ctx, id, prev); err != nil {
		return currentStep, err
	}
	return prev, nil
}

// GoNext подтверждает шаг и переходит к следующему.
func (e *Engine) GoNext(ctx context.Context, id int64, currentStep string, g store.Giveaway) (string, error) {
	next := NextStep(currentStep)
	if next == "" {
		return currentStep, nil
	}
	if err := e.DB.SetDraftStep(ctx, id, next); err != nil {
		return currentStep, err
	}
	return next, nil
}

// ---------- рендер шага (мануал §3 «Общие механики шага») ----------

// StepHeader — «**Шаг N из 10 · Заголовок**\n▰▰…▱▱».
func StepHeader(step string) string {
	n := StepNumber(step)
	return fmt.Sprintf("**Шаг %d из %d · %s %s**\n%s",
		n, TotalSteps(), StepEmoji(step), StepTitle(step), ProgressBar(n, TotalSteps()))
}

// StepQuestion — вопрос шага.
func StepQuestion(step string) string {
	switch step {
	case StepPost:
		return "Пришлите текст поста — оформление сохранится точно как отправите.\nМожно сразу прикрепить фото и/или видео (тем же сообщением)."
	case StepChannel:
		return "Где опубликовать розыгрыш?\nБот может публиковать в любом канале, где он админ."
	case StepName:
		return "Как назовём розыгрыш? Нужно только вам — в списке «Мои розыгрыши»; в посте не появится."
	case StepSubs:
		return "На какие каналы участникам нужно подписаться?\nНе выбрано — розыгрыш без проверки подписки."
	case StepButton:
		return "Что написать на кнопке участия под постом?"
	case StepWinners:
		return "Сколько будет победителей?"
	case StepFinish:
		return "Когда подвести итоги розыгрыша?"
	case StepStart:
		return "Когда опубликовать пост?"
	case StepExtras:
		return "Дополнительные настройки розыгрыша."
	case StepCheck:
		return "Проверьте розыгрыш. Пост выше — как будет выглядеть в канале."
	}
	return ""
}

// TextHint — строка «✍️ …» для шагов, где допустим ввод текстом (мануал:
// «после отправки сразу перейдём к шагу N»).
func TextHint(step string) string {
	switch step {
	case StepPost:
		return "✍️ Отправьте текст и/или фото-видео — после отправки сразу перейдём к шагу 2 «Канал»."
	case StepChannel:
		return "✍️ Или перешлите сюда любой пост из нужного канала (бот должен быть там админом)."
	case StepName:
		return "✍️ Или напишите своё название — после отправки сразу перейдём к шагу 4 «Подписки»."
	case StepButton:
		return "✍️ Или напишите свой текст (до 20 символов)."
	case StepWinners:
		return "✍️ Или напишите число от 1 до 100 — после отправки сразу перейдём к шагу 7 «Итоги»."
	case StepFinish:
		return "✍️ Или отправьте время в формате ДД.ММ.ГГГГ ЧЧ:ММ (можно только ЧЧ:ММ), сейчас " +
			time.Now().In(util.MoscowLocation()).Format("15:04") + " МСК."
	case StepStart:
		return "✍️ Или отправьте дату публикации (ДД.ММ.ГГГГ ЧЧ:ММ или ЧЧ:ММ) — раньше итогов."
	case StepExtras:
		return "✍️ На этом шаге можно ввести лимит друзей числом 1–10 (или сообщение победителю после кнопки ✉️)."
	}
	return ""
}

// SelectedSummary — строка «✓ Выбрано: **…**» по текущим данным черновика.
// Пустая строка = ничего не выбрано.
func (e *Engine) SelectedSummary(ctx context.Context, g store.Giveaway, step string) string {
	switch step {
	case StepPost:
		if g.PostText != "" {
			return "✓ Выбрано: **" + excerpt(g.PostText, 60) + "**"
		}
		if len(g.MediaTokens) > 0 {
			return fmt.Sprintf("✓ Выбрано: **%d вложений без текста**", len(g.MediaTokens))
		}
	case StepChannel:
		if len(g.PublishChannelIDs) > 0 {
			return "✓ Выбрано: **" + e.channelTitles(ctx, g.OwnerUserID, g.PublishChannelIDs) + "**"
		}
	case StepName:
		if g.Name != "" {
			return "✓ Выбрано: **" + g.Name + "**"
		}
	case StepSubs:
		if len(g.SubscribeChannelIDs) > 0 {
			return "✓ Выбрано: **" + e.channelTitles(ctx, g.OwnerUserID, g.SubscribeChannelIDs) + "**"
		}
		return "✓ Выбрано: **без проверки подписки**"
	case StepButton:
		if g.ButtonText != "" {
			return "✓ Выбрано: **" + g.ButtonText + "**"
		}
	case StepWinners:
		if g.WinnersCount > 0 {
			return fmt.Sprintf("✓ Выбрано: **%d**", g.WinnersCount)
		}
	case StepFinish:
		switch {
		case g.FinishMode == "count" && g.FinishParticipantsCount > 0:
			return fmt.Sprintf("✓ Выбрано: **👥 при %d участниках**", g.FinishParticipantsCount)
		case g.FinishMode != "count" && g.FinishAt != nil:
			return "✓ Выбрано: **" + util.FormatMoscowTime(*g.FinishAt) + "**"
		}
	case StepStart:
		if g.PublishNow {
			return "✓ Выбрано: **сразу после «🚀 Опубликовать»**"
		}
		if g.PublishAt != nil {
			return "✓ Выбрано: **" + util.FormatMoscowTime(*g.PublishAt) + "**"
		}
	}
	return ""
}

// StepText — полный текст шага по шаблону мануала:
//
//	**Шаг 6 из 10 · 🏆 Победители**
//	▰▰▰▰▰▰▱▱▱▱
//	**Сколько будет победителей?**
//	✓ Выбрано: **1**
//
//	✍️ Или напишите число от 1 до 100 — …
func (e *Engine) StepText(ctx context.Context, g store.Giveaway, step string) string {
	var sb strings.Builder
	sb.WriteString(StepHeader(step))
	sb.WriteString("\n**")
	sb.WriteString(StepQuestion(step))
	sb.WriteString("**\n")
	if sel := e.SelectedSummary(ctx, g, step); sel != "" {
		sb.WriteString(sel)
		sb.WriteString("\n")
	}
	if step == StepExtras {
		sb.WriteString("\n" + ExtrasStateText(g))
	}
	if hint := TextHint(step); hint != "" {
		sb.WriteString("\n")
		sb.WriteString(hint)
	}
	return sb.String()
}

// ExtrasStateText — блок состояния всех функций шага 9 (мануал §7):
// капча / друзья / сообщение победителю / место итогов.
func ExtrasStateText(g store.Giveaway) string {
	capV := "❌ выключена"
	if g.CaptchaMode == store.CaptchaAll {
		capV = "✅ включена"
	}
	friends := "❌ выключены"
	if g.ReferralEnabled {
		limit := g.ReferralLimit
		if limit <= 0 {
			limit = 10
		}
		friends = fmt.Sprintf("✅ до %d друзей = до +%d билетов", limit, limit)
	}
	msg := "не добавлено"
	if g.WinnersMessage != "" {
		msg = "добавлено"
	}
	results := "в исходном посте"
	if !g.ResultsInOriginalPost {
		results = "отдельным постом"
	}
	return fmt.Sprintf("🛡 **Капча:** %s\n👥 **Приглашение друзей:** %s\n✉️ **Сообщение победителю:** %s\n📍 **Итоги:** %s\n🔔 Победители с включёнными уведомлениями получат сообщение в личку.",
		capV, friends, msg, results)
}

// PassedText — «✓ ответ» для морфинга пройденного шага (клавиатура стирается).
func (e *Engine) PassedText(ctx context.Context, g store.Giveaway, step string) string {
	return StepHeader(step) + "\n" + firstLine(e.SelectedSummary(ctx, g, step))
}

// StepComplete — заполнен ли шаг (для «Далее →»: не заполнен → только уведомление).
func (e *Engine) StepComplete(ctx context.Context, g store.Giveaway, step string) bool {
	switch step {
	case StepPost:
		return g.PostText != "" || len(g.MediaTokens) > 0
	case StepChannel:
		return len(g.PublishChannelIDs) > 0
	case StepName:
		return g.Name != ""
	case StepSubs:
		return true // без каналов = без проверки подписки (легитимно)
	case StepButton:
		return g.ButtonText != ""
	case StepWinners:
		return g.WinnersCount >= 1
	case StepFinish:
		if g.FinishMode == "count" {
			return g.FinishParticipantsCount >= 10
		}
		return g.FinishAt != nil
	case StepStart:
		return g.PublishNow || g.PublishAt != nil
	case StepExtras:
		return true // всё опционально
	}
	return true
}

// MissingText — что именно не заполнено (уведомление на «Далее →»).
func MissingText(step string) string {
	switch step {
	case StepPost:
		return "Сначала пришлите текст поста или фото/видео"
	case StepChannel:
		return "Сначала выберите канал публикации"
	case StepName:
		return "Сначала выберите или напишите название"
	case StepButton:
		return "Сначала выберите текст кнопки"
	case StepFinish:
		return "Сначала выберите время итогов или «👥 По числу участников»"
	case StepStart:
		return "Сначала выберите время публикации"
	}
	return "Сначала заполните шаг"
}

// HintForStep — короткая подсказка (≤150 символов, callback-notification).
func HintForStep(step string) string {
	switch step {
	case StepPost:
		return "Текст ≥ 10 символов или медиа. Оформление (жирный, эмодзи) сохранится как есть."
	case StepChannel:
		return "⚠️ = бот не админ. «➕ Подключить канал» — инструкция и проверка прав."
	case StepName:
		return "Название видно только вам. Варианты собраны из текста поста."
	case StepSubs:
		return "Канал публикации предвыбран и не снимается — участники и так его увидят."
	case StepButton:
		return "Короткий текст 1–20 символов; счётчик участников допишется автоматически."
	case StepWinners:
		return "1–100. Один человек выигрывает не больше одного раза."
	case StepFinish:
		return "Итоги не раньше чем через 10 минут; по числу — от 10 участников."
	case StepStart:
		return "«Сразу» — пост выйдет через ~полминуты после «🚀 Опубликовать»."
	case StepExtras:
		return "Капча — 4 цифры в мини-аппе. Друзья: +1 билет за приглашённого."
	case StepCheck:
		return "«✏️» у пункта — вернуться к его шагу, после «Далее» вернёт сюда."
	}
	return ""
}

// NameVariants — авто-варианты названия (мануал §4 шаг 3): первая строка
// поста + дата / + канал / обрезка первой строки.
func NameVariants(g store.Giveaway, channelTitle string) []string {
	first := strings.TrimSpace(strings.Split(g.PostText, "\n")[0])
	first = strings.TrimLeft(first, "*#->_ ")
	if first == "" {
		first = "Розыгрыш"
	}
	first = excerpt(first, 30)
	d := time.Now().In(util.MoscowLocation()).Format("02.01")
	variants := []string{first + " · " + d}
	if channelTitle != "" {
		variants = append(variants, first+" · "+excerpt(channelTitle, 20))
	}
	return variants
}

// FormatCheck — сводка шага проверки (мануал §15).
func FormatCheck(g store.Giveaway, subs, pubs string) string {
	start := "сразу"
	if !g.PublishNow && g.PublishAt != nil {
		start = util.FormatMoscowTime(*g.PublishAt)
	}
	finish := "—"
	switch {
	case g.FinishMode == "count":
		finish = fmt.Sprintf("при %d участниках", g.FinishParticipantsCount)
	case g.FinishAt != nil:
		finish = util.FormatMoscowTime(*g.FinishAt)
	}
	resultsLoc := "в исходном посте"
	if !g.ResultsInOriginalPost {
		resultsLoc = "отдельным постом"
	}
	captcha := "нет"
	if g.CaptchaMode == store.CaptchaAll {
		captcha = "да"
	}
	contact := "—"
	if g.WinnersMessage != "" {
		contact = "добавлено"
	}
	return "Информация о розыгрыше:\n" +
		"Название: " + nonEmpty(g.Name) + "\n" +
		"Старт: " + start + "\n" +
		"Итоги: " + finish + "\n" +
		"Кол-во победителей: " + fmt.Sprintf("%d", g.WinnersCount) + "\n" +
		"Сообщение победителям: " + contact + "\n" +
		"Результаты: " + resultsLoc + "\n" +
		"Каналы для подписки: " + nonEmpty(subs) + "\n" +
		"Старт будет опубликован в: " + nonEmpty(pubs) + "\n" +
		"Капча: " + captcha + "\n" +
		"Приглашение друзей: " + yesno(g.ReferralEnabled)
}

// FormatPreview — старая сводка (используется тестами/легаси).
func FormatPreview(g store.Giveaway, db *store.Store) string {
	subs := channelNames(db, g.SubscribeChannelIDs, g.OwnerUserID)
	pubs := channelNames(db, g.PublishChannelIDs, g.OwnerUserID)
	return FormatCheck(g, subs, pubs)
}

// ---------- хелперы ----------

// channelTitles — список названий каналов по внутренним id (для ✓ Выбрано).
func (e *Engine) channelTitles(ctx context.Context, ownerUserID int64, ids []int64) string {
	return e.ChannelTitlesLine(ctx, ownerUserID, ids)
}

// ChannelTitlesLine — названия каналов через запятую (для сводки проверки).
func (e *Engine) ChannelTitlesLine(ctx context.Context, ownerUserID int64, ids []int64) string {
	if len(ids) == 0 {
		return "—"
	}
	channels, err := e.DB.ChannelsByIDs(ctx, ownerUserID, ids)
	if err != nil || len(channels) == 0 {
		return "—"
	}
	titles := make([]string, 0, len(channels))
	for _, c := range channels {
		t := c.Title
		if t == "" {
			t = fmt.Sprintf("канал #%d", c.ChatID)
		}
		titles = append(titles, t)
	}
	if len(titles) > 3 {
		return strings.Join(titles[:3], ", ") + fmt.Sprintf(" и ещё %d", len(titles)-3)
	}
	return strings.Join(titles, ", ")
}

// channelNames — текстовый список каналов (легаси-хелпер для FormatPreview).
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

// excerpt — первые max рун строки в одну строку.
func excerpt(s string, max int) string {
	s = strings.Join(strings.Split(s, "\n"), " ")
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return string(runes[:max-1]) + "…"
}

// firstLine — первая строка (или пустая).
func firstLine(s string) string {
	if s == "" {
		return ""
	}
	return strings.SplitN(s, "\n", 2)[0]
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
