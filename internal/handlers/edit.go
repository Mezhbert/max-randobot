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
// Каждое поле хранится в БД отдельно — правится точечно. После правки полей,
// влияющих на опубликованный пост (текст/медиа/кнопка), посты live-обновляются.

// editFieldDef — редактируемое поле: заголовок + колонка БД + тип ввода.
type editFieldDef struct {
	title      string // заголовок кнопки
	col        string // колонка БД
	kind       string // text | date | number | channels | media | captcha | finishmode | resultsloc
	skipActive bool   // недоступно для active (например время публикации)
	refresh    bool   // после правки обновить опубликованные посты (live-edit)
}

var editFields = map[string]editFieldDef{
	"name":          {title: "🏷 Название", col: "name", kind: "text"},
	"post_text":     {title: "📝 Текст поста", col: "post_text", kind: "text", refresh: true},
	"media":         {title: "🖼 Медиа", col: "media_tokens", kind: "media", refresh: true},
	"button_text":   {title: "🔘 Текст кнопки", col: "button_text", kind: "text", refresh: true},
	"sub_channels":  {title: "📢 Каналы подписки", col: "subscribe_channel_ids", kind: "channels"},
	"pub_channels":  {title: "📣 Каналы публикации", col: "publish_channel_ids", kind: "channels", skipActive: true},
	"publish_at":    {title: "📅 Время публикации", col: "publish_at", kind: "date", skipActive: true},
	"finish":        {title: "🏁 Условие итогов", col: "finish_mode", kind: "finishmode"},
	"finish_at":     {title: "⏰ Дата итогов", col: "finish_at", kind: "date"}, // вводится через смену режима, кнопки в меню нет
	"winners":       {title: "🏆 Кол-во победителей", col: "winners_count", kind: "number"},
	"winners_msg":   {title: "✉️ Сообщение победителям", col: "winners_message", kind: "text"},
	"results_loc":   {title: "📍 Где итоги", col: "results_in_original_post", kind: "resultsloc"},
	"results_text":  {title: "📄 Текст итогов", col: "results_post_text", kind: "text"},
	"captcha":       {title: "🤖 Капча", col: "captcha_mode", kind: "captcha"},
}

// orderedEditKeys — стабильный порядок кнопок меню редактирования.
var orderedEditKeys = []string{
	"name", "post_text", "media", "button_text",
	"sub_channels", "pub_channels",
	"publish_at", "finish",
	"winners", "winners_msg",
	"results_loc", "results_text",
	"captcha",
}

// showEditMenu — меню редактирования розыгрыша по полям.
func (b *Bot) showEditMenu(ctx context.Context, uid, id int64) {
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
	_, _ = b.api.SendTextWithKeyboard(ctx, uid, text, kb)
}

// startEditField — показывает текущее значение поля и переводит в режим ввода.
func (b *Bot) startEditField(ctx context.Context, uid int64, g store.Giveaway, key string) {
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
		b.showCaptchaEditOptions(ctx, uid, g)
		return
	case "finishmode":
		b.showFinishEditOptions(ctx, uid, g)
		return
	case "resultsloc":
		b.showResultsLocEditOptions(ctx, uid, g)
		return
	case "channels":
		b.showChannelsEditOptions(ctx, uid, g, key)
		return
	case "media":
		b.startEditMedia(ctx, uid, g)
		return
	}

	// Текст/дата/число — показываем текущее значение и ждём ввод.
	cur := b.fieldCurrentValue(g, key)
	_ = b.db.SetSessionPayload(ctx, uid, store.SessionEditField, map[string]any{
		"giveaway_id": float64(g.ID),
		"field":       key,
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
	case "media":
		if len(g.MediaTokens) == 0 {
			return "— (нет медиа)"
		}
		return fmt.Sprintf("%d вложений", len(g.MediaTokens))
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
		_ = b.api.SendText(ctx, uid, "✅ Сохранено.")
		b.showEditMenu(ctx, uid, g.ID)
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
	_ = b.api.SendText(ctx, uid, "✅ Сохранено.")
	if def.refresh {
		b.refreshPublishedPosts(ctx, g.ID)
	}
	b.showEditMenu(ctx, uid, g.ID)
	return true
}

// --- Выбор-поля: показываем опции кнопками, ввод текстом не нужен ---

func (b *Bot) showCaptchaEditOptions(ctx context.Context, uid int64, g store.Giveaway) {
	kb := model.NewKeyboard()
	kb.AddRow().
		AddCallBack("✅ Да", util.CBGwEditSet+"captcha:all:"+fmt.Sprintf("%d", g.ID)).
		AddCallBack("❌ Нет", util.CBGwEditSet+"captcha:none:"+fmt.Sprintf("%d", g.ID))
	kb.AddRow().AddCallBack("← Назад", util.CBGwEdit+"captcha:"+fmt.Sprintf("%d", g.ID))
	_, _ = b.api.SendTextWithKeyboard(ctx, uid, "🤖 Капча при участии?", kb)
}

func (b *Bot) showFinishEditOptions(ctx context.Context, uid int64, g store.Giveaway) {
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

func (b *Bot) showResultsLocEditOptions(ctx context.Context, uid int64, g store.Giveaway) {
	kb := model.NewKeyboard()
	kb.AddRow().
		AddCallBack("В исходном посте", util.CBGwEditSet+"results_original:1:"+fmt.Sprintf("%d", g.ID)).
		AddCallBack("Отдельным постом", util.CBGwEditSet+"results_original:0:"+fmt.Sprintf("%d", g.ID))
	kb.AddRow().AddCallBack("← Назад", util.CBGwEdit+"results_loc:"+fmt.Sprintf("%d", g.ID))
	_, _ = b.api.SendTextWithKeyboard(ctx, uid, "📍 Где публиковать итоги?", kb)
}

// showChannelsEditOptions — редактирование каналов (подписки/публикации)
// тем же пикером с галочками, что и при создании (edit-in-place по mid).
func (b *Bot) showChannelsEditOptions(ctx context.Context, uid int64, g store.Giveaway, key string) {
	isPub := key == "pub_channels"
	// Пусто — берём сохранённый mid пикера (edit-in-place) или шлём новое.
	prefix := util.CBGwEditChTglSub
	title := "📢 Каналы для подписки"
	if isPub {
		prefix = util.CBGwEditChTglPub
		title = "📣 Каналы публикации"
	}

	channels, _ := b.db.ListChannels(ctx, uid)
	selected := g.SubscribeChannelIDs
	if isPub {
		selected = g.PublishChannelIDs
	}

	kb := model.NewKeyboard()
	for _, c := range channels {
		mark := "⬜ "
		for _, sid := range selected {
			if sid == c.ID {
				mark = "✅ "
				break
			}
		}
		ctitle := c.Title
		if ctitle == "" {
			ctitle = fmt.Sprintf("канал #%d", c.ChatID)
		}
		if !c.IsBotAdmin {
			ctitle += " ⚠️"
		}
		kb.AddRow().AddCallBack(mark+ctitle, fmt.Sprintf("%s%d:%d", prefix, c.ID, g.ID))
	}
	kb.AddRow().AddCallBack("✅ Готово", util.CBGwEdit+fmt.Sprintf("%d", g.ID))
	kb.AddRow().AddCallBack("← К списку полей", util.CBGwEditField+key+":"+fmt.Sprintf("%d", g.ID))

	text := title + " розыгрыша «" + g.Name + "».\n\n" +
		"Нажимайте на каналы, чтобы отметить/снять. ⚠️ — бот НЕ админ (нельзя использовать).\n" +
		"Готово — «✅ Готово»."
	_, _ = b.api.SendTextWithKeyboard(ctx, uid, text, kb)
}

// startEditMedia — редактирование медиа: кнопки «заменить» (отправить заново)
// и «очистить».
func (b *Bot) startEditMedia(ctx context.Context, uid int64, g store.Giveaway) {
	cur := "нет"
	if len(g.MediaTokens) > 0 {
		cur = fmt.Sprintf("%d вложений", len(g.MediaTokens))
	}
	kb := model.NewKeyboard()
	kb.AddRow().AddCallBack("🖼 Отправить новые медиа", util.CBGwEditSet+"media:replace:"+fmt.Sprintf("%d", g.ID))
	kb.AddRow().AddCallBack("🗑 Убрать все медиа", util.CBGwEditSet+"media:clear:"+fmt.Sprintf("%d", g.ID))
	kb.AddRow().AddCallBack("← К списку полей", util.CBGwEditField+"media:"+fmt.Sprintf("%d", g.ID))
	_, _ = b.api.SendTextWithKeyboard(ctx, uid,
		fmt.Sprintf("🖼 Медиа (сейчас: %s).\n\n«Отправить новые» — пришлите фото/видео заново (старые заменятся).", cur), kb)
}

// startEditMediaInput — переводит в режим ввода медиа (SessionEditMedia):
// следующие сообщения с фото/видео добавляются к набору, «✅ Готово» завершает.
func (b *Bot) startEditMediaInput(ctx context.Context, uid int64, g store.Giveaway) {
	_ = b.db.SetSessionPayload(ctx, uid, store.SessionEditMedia, map[string]any{
		"giveaway_id": float64(g.ID),
	})
	_, _ = b.api.SendTextWithKeyboard(ctx, uid,
		"🖼 Пришлите фото и/или видео для поста (по одному в сообщении).\n"+
			"Когда закончите — нажмите «✅ Готово».",
		util.MediaEditDoneKeyboard(g.ID))
}

// handleEditMediaInput — приём медиа в режиме редактуры. Возвращает true если
// сообщение обработано.
func (b *Bot) handleEditMediaInput(ctx context.Context, uid int64, msg model.MessageUpdate) bool {
	sess, err := b.db.GetSessionPayload(ctx, uid)
	if err != nil || sess.State != store.SessionEditMedia {
		return false
	}
	gidF, _ := sess.Payload["giveaway_id"].(float64)
	g, err := b.db.GetGiveaway(ctx, int64(gidF), uid)
	if err != nil {
		_ = b.db.ClearSession(ctx, uid)
		_ = b.api.SendText(ctx, uid, "Розыгрыш не найден.")
		return true
	}

	var tokens []store.MediaToken
	for _, at := range msg.Body.Attachments {
		switch at.Type {
		case model.AttachImage:
			if at.Payload.Token != "" {
				tokens = append(tokens, store.MediaToken{Token: at.Payload.Token, Type: "image"})
			}
		case model.AttachVideo:
			if at.Payload.Token != "" {
				tokens = append(tokens, store.MediaToken{Token: at.Payload.Token, Type: "video"})
			}
		}
	}
	if len(tokens) == 0 {
		_, _ = b.api.SendTextWithKeyboard(ctx, uid, "Не увидел фото/видео во вложениях. Пришлите медиа или нажмите «✅ Готово».",
			util.MediaEditDoneKeyboard(g.ID))
		return true
	}
	all := append(append([]store.MediaToken{}, g.MediaTokens...), tokens...)
	if err := b.db.UpdateGiveawayFieldAny(ctx, g.ID, "media_tokens", all); err != nil {
		_ = b.api.SendText(ctx, uid, "⚠️ Ошибка сохранения: "+err.Error())
		return true
	}
	// Подтверждение с кнопкой «Готово» на каждом шаге приёма — флоу не сбивается.
	_, _ = b.api.SendTextWithKeyboard(ctx, uid,
		fmt.Sprintf("✅ Медиа добавлено (%d всего). Ещё или «✅ Готово».",
			len(all)), util.MediaEditDoneKeyboard(g.ID))
	return true
}

// finishEditMedia — завершение ввода медиа: live-обновление постов и меню.
func (b *Bot) finishEditMedia(ctx context.Context, uid, gid int64) {
	_ = b.db.ClearSession(ctx, uid)
	g, err := b.db.GetGiveaway(ctx, gid, uid)
	if err != nil {
		_ = b.api.SendText(ctx, uid, "Розыгрыш не найден.")
		return
	}
	_ = b.api.SendText(ctx, uid, "✅ Медиа сохранено.")
	b.refreshPublishedPosts(ctx, g.ID)
	b.showEditMenu(ctx, uid, g.ID)
}

// refreshPublishedPosts — live-обновление опубликованных постов розыгрыша
// (текст/медиа/кнопка) после правки. Для scheduled — no-op (постов ещё нет).
func (b *Bot) refreshPublishedPosts(ctx context.Context, gid int64) {
	if b.counterUpd == nil {
		return
	}
	g, err := b.db.GetGiveawayAny(ctx, gid)
	if err != nil || g.Status != store.StatusActive || len(g.PublishMessageIDs) == 0 {
		return
	}
	go b.counterUpd(context.Background(), &g) // переиспользуем механизм счётчика
}

// applyEditSet — применение выбор-опции (captcha/finish/результаты/медиа).
func (b *Bot) applyEditSet(ctx context.Context, uid int64, key, value string, gid int64) {
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
			if value == "time" && g.FinishAt == nil {
				// Даты ещё нет — запрашиваем (иначе режим «по времени» без даты).
				_ = b.db.SetSessionPayload(ctx, uid, store.SessionEditField, map[string]any{
					"giveaway_id": float64(g.ID),
					"field":       "finish_at",
				})
				_, _ = b.api.SendTextWithKeyboard(ctx, uid,
					"⏰ Введите дату итогов (ЧЧ:ММ или ДД.ММ.ГГГГ ЧЧ:ММ, МСК):", util.CancelEditKeyboard(g.ID))
				return
			}
		}
	case "results_original":
		bval := value == "1"
		_ = b.db.UpdateGiveawayFieldAny(ctx, g.ID, "results_in_original_post", bval)
	case "media":
		switch value {
		case "clear":
			_ = b.db.UpdateGiveawayFieldAny(ctx, g.ID, "media_tokens", []store.MediaToken{})
			_ = b.api.SendText(ctx, uid, "✅ Медиа убраны.")
			b.refreshPublishedPosts(ctx, g.ID)
			b.showEditMenu(ctx, uid, g.ID)
			return
		case "replace":
			// Очищаем и переводим в режим приёма медиа.
			_ = b.db.UpdateGiveawayFieldAny(ctx, g.ID, "media_tokens", []store.MediaToken{})
			b.startEditMediaInput(ctx, uid, g)
			return
		}
	}
	_ = b.api.SendText(ctx, uid, "✅ Сохранено.")
	b.showEditMenu(ctx, uid, g.ID)
}

// toggleEditChannel — тоггл канала в редактуре (sub/pub) с перерисовкой пикера.
func (b *Bot) toggleEditChannel(ctx context.Context, uid int64, p string) {
	isSub := strings.HasPrefix(p, util.CBGwEditChTglSub)
	rest := strings.TrimPrefix(strings.TrimPrefix(p, util.CBGwEditChTglSub), util.CBGwEditChTglPub)
	parts := strings.Split(rest, ":")
	if len(parts) != 2 {
		return
	}
	chID, _ := strconv.ParseInt(parts[0], 10, 64)
	gid, _ := strconv.ParseInt(parts[1], 10, 64)

	g, err := b.db.GetGiveaway(ctx, gid, uid)
	if err != nil || (g.Status != store.StatusScheduled && g.Status != store.StatusActive) {
		_ = b.api.SendText(ctx, uid, "Розыгрыш не найден.")
		return
	}

	field := "subscribe_channel_ids"
	current := g.SubscribeChannelIDs
	key := "sub_channels"
	if !isSub {
		field = "publish_channel_ids"
		current = g.PublishChannelIDs
		key = "pub_channels"
	}

	found := false
	var updated []int64
	for _, id := range current {
		if id == chID {
			found = true
			continue
		}
		updated = append(updated, id)
	}
	if !found {
		// Выбор: не-админ канал нельзя выбрать (и обязательно ≥1 канал публикации).
		if ch, err := b.db.GetChannel(ctx, chID, uid); err == nil && !ch.IsBotAdmin {
			_ = b.api.SendText(ctx, uid, "⚠️ Бот не админ в этом канале — выбрать нельзя.")
			return
		}
		updated = append(updated, chID)
	}
	if !isSub && len(updated) == 0 {
		_ = b.api.SendText(ctx, uid, "⚠️ Нужен хотя бы один канал публикации.")
		return
	}

	if err := b.db.UpdateGiveawayFieldAny(ctx, g.ID, field, updated); err != nil {
		_ = b.api.SendText(ctx, uid, "⚠️ Ошибка обновления: "+err.Error())
		return
	}
	// Перерисовываем пикер с актуальным выбором.
	gg, _ := b.db.GetGiveaway(ctx, gid, uid)
	b.showChannelsEditOptions(ctx, uid, gg, key)
}

// escapeMarkdown экранирует скобки в markdown-упоминании.
func escapeMarkdown(s string) string {
	r := strings.NewReplacer("[", "\\[", "]", "\\]", "(", "\\(", ")", "\\)")
	return r.Replace(s)
}

// pickExtraWinner — выбор дополнительного победителя в завершённом розыгрыше
// (если основной не отвечает или оказался ботом). Weighted-выбор среди всех
// участников, исключая текущих победителей. Публикует доп. итоги в каналы.
func (b *Bot) pickExtraWinner(ctx context.Context, uid, gid int64) {
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
	mention := fmt.Sprintf("[%s](max://user/%d)", escapeMarkdown(name), extra[0])
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
