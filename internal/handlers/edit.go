package handlers

import (
	"context"
	"fmt"
	"math/rand"
	"strconv"
	"strings"
	"time"

	"github.com/maxbot/giveawaybot/internal/draw"
	"github.com/maxbot/giveawaybot/internal/store"
	"github.com/maxbot/giveawaybot/internal/util"
	"github.com/max-messenger/max-bot-api-client-go/v2/model"
)

// Редактура scheduled/active розыгрышей по полям (без перепрохождения FSM).
// Каждое поле хранится в БД отдельно — правится точечно.

// editFields — редактируемые поля: ключ payload → заголовок + колонка.
// Для active-розыгрышей publish_at недоступен (пост уже вышел).
type editFieldDef struct {
	title    string // заголовок кнопки
	col      string // колонка БД
	kind     string // text | date | number | channels | captcha | finishmode
	skipActive bool // недоступно для active (например время публикации)
}

var editFields = map[string]editFieldDef{
	"name":         {title: "🏷 Название", col: "name", kind: "text"},
	"post_text":    {title: "📝 Текст поста", col: "post_text", kind: "text"},
	"button_text":  {title: "🔘 Текст кнопки", col: "button_text", kind: "text"},
	"publish_at":   {title: "📅 Время публикации", col: "publish_at", kind: "date", skipActive: true},
	"finish":       {title: "🏁 Условие итогов", col: "finish_mode", kind: "finishmode"},
	"winners":      {title: "🏆 Кол-во победителей", col: "winners_count", kind: "number"},
	"winners_msg":  {title: "✉️ Сообщение победителям", col: "winners_message", kind: "text"},
	"results_loc":  {title: "📍 Где итоги", col: "results_in_original_post", kind: "resultsloc"},
	"results_text": {title: "📄 Текст итогов", col: "results_post_text", kind: "text"},
	"captcha":      {title: "🤖 Капча", col: "captcha_mode", kind: "captcha"},
	"sub_channels": {title: "📢 Каналы подписки", col: "subscribe_channel_ids", kind: "channels"},
}

// orderedEditKeys — стабильный порядок кнопок меню редактирования.
var orderedEditKeys = []string{
	"name", "post_text", "button_text",
	"sub_channels",
	"publish_at", "finish",
	"winners", "winners_msg",
	"results_loc", "results_text",
	"captcha",
}

// showEditMenuSaved — меню редактирования с пометкой о сохранении (одно сообщение).
func (b *Bot) showEditMenuSaved(ctx context.Context, uid, id int64, note string) {
	// Пере-используем showEditMenu, добавляя пометку в заголовок через обёртку.
	g, err := b.db.GetGiveaway(ctx, id, uid)
	if err != nil {
		_ = b.api.SendText(ctx, uid, "Розыгрыш не найден.")
		return
	}
	kb := model.NewKeyboard()
	for _, key := range orderedEditKeys {
		def := editFields[key]
		if g.Status == store.StatusActive && def.skipActive {
			continue
		}
		kb.AddRow().AddCallBack(def.title, util.CBGwEditField+key+":"+fmt.Sprintf("%d", g.ID))
	}
	kb.AddRow().AddCallBack("✅ Готово", util.CBGwOpen+fmt.Sprintf("%d", g.ID))
	kb.AddRow().AddCallBack("← К списку", util.CBGwList)
	_, _ = b.api.SendTextWithKeyboard(ctx, uid,
		note+"\n\n✏ Редактирование «"+g.Name+"». Выберите, что ещё изменить:", kb)
}

// showEditMenu — меню редактирования розыгрыша по полям.
func (b *Bot) showEditMenu(ctx context.Context, uid, id int64, cbID string) {
	g, err := b.db.GetGiveaway(ctx, id, uid)
	if err != nil {
		_ = b.api.SendText(ctx, uid, "Розыгрыш не найден.")
		return
	}
	if g.Status != store.StatusScheduled && g.Status != store.StatusActive {
		_ = b.api.SendText(ctx, uid, "Редактировать можно только запланированные и текущие розыгрыши.")
		return
	}

	kb := model.NewKeyboard()
	for _, key := range orderedEditKeys {
		def := editFields[key]
		if g.Status == store.StatusActive && def.skipActive {
			continue // время публикации недоступно для вышедших
		}
		kb.AddRow().AddCallBack(def.title, util.CBGwEditField+key+":"+fmt.Sprintf("%d", g.ID))
	}
	kb.AddRow().AddCallBack("✅ Готово", util.CBGwOpen+fmt.Sprintf("%d", g.ID))
	kb.AddRow().AddCallBack("← К списку", util.CBGwList)

	text := fmt.Sprintf("✏ Редактирование «%s»\n\nВыберите, что изменить:", g.Name)
	b.render(ctx, uid, cbID, text, kb)
}

// startEditField — показывает текущее значение поля и переводит в режим ввода.
func (b *Bot) startEditField(ctx context.Context, uid int64, cbID string, g store.Giveaway, key string) {
	def, ok := editFields[key]
	if !ok {
		return
	}
	if g.Status == store.StatusActive && def.skipActive {
		_ = b.api.SendText(ctx, uid, "Это поле нельзя менять у активного розыгрыша.")
		return
	}

	// Для выбор-полей (captcha/finishmode/resultsloc) показываем опции кнопками.
	switch def.kind {
	case "captcha":
		b.showCaptchaEditOptions(ctx, uid, cbID, g)
		return
	case "finishmode":
		b.showFinishEditOptions(ctx, uid, cbID, g)
		return
	case "resultsloc":
		b.showResultsLocEditOptions(ctx, uid, cbID, g)
		return
	case "channels":
		b.showChannelsEditOptions(ctx, uid, g, key)
		return
	}

	// Текст/дата/число — показываем текущее значение и ждём ввод.
	cur := b.fieldCurrentValue(g, key)
	_ = b.db.SetSessionPayload(ctx, uid, store.SessionEditField, map[string]any{
		"giveaway_id": float64(g.ID),
		"field":        key,
	})
	prompt := fmt.Sprintf("%s\n\nТекущее значение:\n%s\n\nОтправьте новое значение:", def.title, cur)
	if def.kind == "date" {
		prompt += "\n(формат ЧЧ:ММ или ДД.ММ.ГГГГ ЧЧ:ММ, по МСК)"
	}
	if def.kind == "number" {
		prompt += "\n(целое число)"
	}
	_, _ = b.api.SendTextWithKeyboard(ctx, uid, prompt, util.CancelEditKeyboard(g.ID))
}

// fieldCurrentValue — строковое представление текущего значения поля.
func (b *Bot) fieldCurrentValue(g store.Giveaway, key string) string {
	switch key {
	case "name":
		return g.Name
	case "post_text":
		return truncate(g.PostText, 300)
	case "button_text":
		return g.ButtonText
	case "publish_at":
		if g.PublishAt != nil {
			return util.FormatMoscowTime(*g.PublishAt)
		}
		return "—"
	case "winners":
		return fmt.Sprintf("%d", g.WinnersCount)
	case "winners_msg":
		if g.WinnersMessage == "" {
			return "— (не задано)"
		}
		return truncate(g.WinnersMessage, 200)
	case "results_text":
		if g.ResultsPostText == "" {
			return "— (стандартный)"
		}
		return truncate(g.ResultsPostText, 200)
	}
	return "—"
}

// handleEditFieldInput — ввод нового значения поля (режим SessionEditField).
// Возвращает true если ввод обработан.
func (b *Bot) handleEditFieldInput(ctx context.Context, uid int64, text string) bool {
	sess, err := b.db.GetSessionPayload(ctx, uid)
	if err != nil || sess.State != store.SessionEditField {
		_ = b.db.ClearSession(ctx, uid)
		return false
	}
	gidF, _ := sess.Payload["giveaway_id"].(float64)
	fieldKey, _ := sess.Payload["field"].(string)
	gid := int64(gidF)

	g, err := b.db.GetGiveaway(ctx, gid, uid)
	if err != nil {
		_ = b.db.ClearSession(ctx, uid)
		_ = b.api.SendText(ctx, uid, "Розыгрыш не найден.")
		return true
	}

	// Особый случай: ввод целевого числа участников (finish_mode=count).
	if fieldKey == "finish_count" {
		n, err := strconv.Atoi(strings.TrimSpace(text))
		if err != nil || n < 2 {
			_ = b.api.SendText(ctx, uid, "⚠️ Введите целое число ≥ 2.")
			return true
		}
		_ = b.db.UpdateGiveawayFieldAny(ctx, g.ID, "finish_participants_count", n)
		_ = b.db.ClearSession(ctx, uid)
		b.showEditMenuSaved(ctx, uid, g.ID, "✅ Целевое число участников сохранено")
		return true
	}

	def, ok := editFields[fieldKey]
	if !ok {
		_ = b.db.ClearSession(ctx, uid)
		return true
	}

	// Валидация и запись по типу поля.
	var val any
	switch def.kind {
	case "date":
		t, err := util.ParseMoscowTime(strings.TrimSpace(text))
		if err != nil {
			_ = b.api.SendText(ctx, uid, "⚠️ "+err.Error())
			return true
		}
		if fieldKey == "publish_at" && !t.After(time.Now()) {
			_ = b.api.SendText(ctx, uid, "⚠️ Время публикации должно быть в будущем.")
			return true
		}
		if fieldKey == "finish_at" && g.PublishAt != nil && !t.After(*g.PublishAt) {
			_ = b.api.SendText(ctx, uid, "⚠️ Итоги должны быть позже публикации.")
			return true
		}
		val = t
	case "number":
		n, err := strconv.Atoi(strings.TrimSpace(text))
		if err != nil || n < 1 {
			_ = b.api.SendText(ctx, uid, "⚠️ Введите целое число ≥ 1.")
			return true
		}
		if fieldKey == "winners" && n > 100 {
			_ = b.api.SendText(ctx, uid, "⚠️ Максимум 100 победителей.")
			return true
		}
		val = n
	default: // text
		if strings.TrimSpace(text) == "" {
			_ = b.api.SendText(ctx, uid, "⚠️ Значение не может быть пустым.")
			return true
		}
		val = text
	}

	if err := b.db.UpdateGiveawayFieldAny(ctx, g.ID, def.col, val); err != nil {
		_ = b.api.SendText(ctx, uid, "⚠️ Ошибка сохранения: "+err.Error())
		return true
	}
	_ = b.db.ClearSession(ctx, uid)
	b.showEditMenuSaved(ctx, uid, g.ID, "✅ Сохранено")
	return true
}

// --- Выбор-поля: показываем опциями, ввод текстом не нужен ---

func (b *Bot) showCaptchaEditOptions(ctx context.Context, uid int64, cbID string, g store.Giveaway) {
	kb := model.NewKeyboard()
	kb.AddRow().
		AddCallBack("✅ Да", util.CBGwEditSet+"captcha:all:"+fmt.Sprintf("%d", g.ID)).
		AddCallBack("❌ Нет", util.CBGwEditSet+"captcha:none:"+fmt.Sprintf("%d", g.ID))
	kb.AddRow().AddCallBack("← Назад", util.CBGwEdit+"captcha:"+fmt.Sprintf("%d", g.ID))
	_, _ = b.api.SendTextWithKeyboard(ctx, uid, "🤖 Капча при участии?", kb)
}

func (b *Bot) showFinishEditOptions(ctx context.Context, uid int64, cbID string, g store.Giveaway) {
	kb := model.NewKeyboard()
	kb.AddRow().
		AddCallBack("⏰ По времени", util.CBGwEditSet+"finish:time:"+fmt.Sprintf("%d", g.ID)).
		AddCallBack("👥 По количеству", util.CBGwEditSet+"finish:count:"+fmt.Sprintf("%d", g.ID))
	kb.AddRow().AddCallBack("← Назад", util.CBGwEdit+"finish:"+fmt.Sprintf("%d", g.ID))
	cur := "по времени"
	if g.FinishMode == "count" {
		cur = fmt.Sprintf("по количеству (%d участников)", g.FinishParticipantsCount)
	}
	_, _ = b.api.SendTextWithKeyboard(ctx, uid, "🏁 Условие завершения (сейчас: "+cur+")?", kb)
}

func (b *Bot) showResultsLocEditOptions(ctx context.Context, uid int64, cbID string, g store.Giveaway) {
	kb := model.NewKeyboard()
	kb.AddRow().
		AddCallBack("В исходном посте", util.CBGwEditSet+"results_original:1:"+fmt.Sprintf("%d", g.ID)).
		AddCallBack("Отдельным постом", util.CBGwEditSet+"results_original:0:"+fmt.Sprintf("%d", g.ID))
	kb.AddRow().AddCallBack("← Назад", util.CBGwEdit+"results_loc:"+fmt.Sprintf("%d", g.ID))
	_, _ = b.api.SendTextWithKeyboard(ctx, uid, "📍 Где публиковать итоги?", kb)
}

func (b *Bot) showChannelsEditOptions(ctx context.Context, uid int64, g store.Giveaway, key string) {
	// Каналы подписки редактируем через отдельный экран с галочками (заготовка —
	// полный тоггл-экран это этап отдельной итерации, пока предупреждаем).
	_ = b.api.SendText(ctx, uid,
		"📢 Редактирование каналов подписки пока выполняется при создании розыгрыша.\n"+
			"Скоро добавим отдельный экран выбора.")
}

// applyEditSet — применение выбор-опции (captcha/finish/результаты).
func (b *Bot) applyEditSet(ctx context.Context, uid int64, cbID string, key, value string, gid int64) {
	g, err := b.db.GetGiveaway(ctx, gid, uid)
	if err != nil {
		_ = b.api.SendText(ctx, uid, "Розыгрыш не найден.")
		return
	}
	switch key {
	case "captcha":
		if value == "all" || value == "none" {
			_ = b.db.UpdateGiveawayFieldAny(ctx, g.ID, "captcha_mode", value)
		}
	case "finish":
		if value == "time" || value == "count" {
			_ = b.db.UpdateGiveawayFieldAny(ctx, g.ID, "finish_mode", value)
			if value == "count" && g.FinishParticipantsCount == 0 {
				// Нужно число — переводим в режим ввода.
				_ = b.db.SetSessionPayload(ctx, uid, store.SessionEditField, map[string]any{
					"giveaway_id": float64(g.ID),
					"field":       "finish_count",
				})
				_, _ = b.api.SendTextWithKeyboard(ctx, uid,
					"👥 Введите целевое число участников:", util.CancelEditKeyboard(g.ID))
				return
			}
		}
	case "results_original":
		bval := value == "1"
		_ = b.db.UpdateGiveawayFieldAny(ctx, g.ID, "results_in_original_post", bval)
	}
	b.showEditMenuSaved(ctx, uid, g.ID, "✅ Сохранено")
}

// pickExtraWinner — выбор дополнительного победителя в завершённом розыгрыше
// (если основной не отвечает или оказался ботом). Weighted-выбор среди всех
// участников, исключая существующих победителей. Публикует доп. итоги в каналы.
func (b *Bot) pickExtraWinner(ctx context.Context, uid, gid int64, cbID string) {
	g, err := b.db.GetGiveaway(ctx, gid, uid)
	if err != nil || g.Status != store.StatusFinished {
		_ = b.api.SendText(ctx, uid, "Доп. победителя можно выбрать только в завершённом розыгрыше.")
		return
	}

	// Участники, не являющиеся текущими победителями.
	tickets, err := b.db.ListParticipantsForDraw(ctx, g.ID)
	if err != nil {
		_ = b.api.SendText(ctx, uid, "⚠️ Ошибка загрузки участников.")
		return
	}
	current := make(map[int64]bool, len(g.WinnersUserIDs))
	for _, w := range g.WinnersUserIDs {
		current[w] = true
	}
	var entries []draw.Entry
	for _, t := range tickets {
		if !current[t.UserID] {
			entries = append(entries, draw.Entry{UserID: t.UserID, Tickets: t.Tickets})
		}
	}
	if len(entries) == 0 {
		_ = b.api.SendText(ctx, uid, "Нет кандидатов: все участники уже победители.")
		return
	}

	extra := draw.Weighted(entries, 1, rand.New(rand.NewSource(time.Now().UnixNano())))
	if len(extra) == 0 {
		_ = b.api.SendText(ctx, uid, "⚠️ Не удалось выбрать победителя.")
		return
	}

	// Дописываем к победителям.
	all := append(append([]int64{}, g.WinnersUserIDs...), extra...)
	if err := b.db.SaveWinners(ctx, g.ID, all); err != nil {
		_ = b.api.SendText(ctx, uid, "⚠️ Ошибка сохранения: "+err.Error())
		return
	}

	// Резолвим имя и оповещаем.
	u, _ := b.db.GetUser(ctx, extra[0])
	name := fmt.Sprintf("Участник #%d", extra[0])
	if u.FirstName != "" {
		name = u.FirstName
	}
	mention := fmt.Sprintf("[%s](max://user/%d)", name, extra[0])
	_ = b.api.SendText(ctx, uid, fmt.Sprintf("🎲 Доп. победитель: %s", mention))

	// Уведомляем победителя в ЛС (если задано сообщение).
	if g.WinnersMessage != "" {
		_ = b.api.SendText(ctx, extra[0],
			fmt.Sprintf("🎉 Вы — дополнительный победитель розыгрыша «%s»!\n\n%s", g.Name, g.WinnersMessage))
	}

	// Отдельное сообщение в каналы публикации (edit старых постов ненадёжен
	// из-за лимита времени редактирования).
	channels, _ := b.db.ChannelsByIDs(ctx, uid, g.PublishChannelIDs)
	for _, ch := range channels {
		_, _ = b.api.SendToChat(ctx, ch.ChatID, fmt.Sprintf("🎲 Доп. победитель розыгрыша: %s", mention), nil, "markdown")
	}
}
